// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/presence"
)

// Locator resolves the live ingress of a device. It is implemented by
// *presence.Service.
type Locator interface {
	Lookup(ctx context.Context, id presence.Identity) (presence.Location, error)
}

// Tuning holds command service operational settings from configuration.
type Tuning struct {
	// PersistentTTL bounds how long an undelivered persistent command waits.
	PersistentTTL time.Duration

	// DeliveryTimeout bounds the wait for the ingress delivery ack of a
	// one-way command and the transport-level response generally.
	DeliveryTimeout time.Duration

	// SweepInterval is the interval of the expiry sweeper.
	SweepInterval time.Duration

	// ClaimBatchSize bounds commands claimed per reconnect event.
	ClaimBatchSize int
}

// ModuleState reports whether the add-on module for a device protocol is
// enrolled, enabled and heartbeat-fresh. It is implemented by an adapter
// over the module registry; nil means no add-on protocols are available.
type ModuleState interface {
	IsOnline(ctx context.Context, protocol string) (bool, error)
}

// addOnProtocols names protocols served by optional modules rather than
// the embedded MQTT/HTTP ingresses.
var addOnProtocols = map[string]struct{}{
	"COAP":  {},
	"LWM2M": {},
	"SNMP":  {},
}

// Router dispatches commands through presence lookup and Core NATS.
type Router struct {
	nc      *nats.Conn
	store   *Store
	locator Locator
	modules ModuleState
	tune    Tuning

	// instanceID identifies claims made by this router process.
	instanceID string

	// metrics records terminal command outcomes.
	metrics commandMetrics
}

// NewRouter constructs a command router.
func NewRouter(nc *nats.Conn, store *Store, locator Locator, tune Tuning) *Router {
	return &Router{
		nc:         nc,
		store:      store,
		locator:    locator,
		tune:       tune,
		instanceID: newInstanceID(),
		metrics:    noopMetrics{},
	}
}

// SetModuleState wires the add-on module state lookup.
func (r *Router) SetModuleState(state ModuleState) {
	r.modules = state
}

// SetMetrics wires command metrics.
func (r *Router) SetMetrics(metrics commandMetrics) {
	r.metrics = metrics
}

// Dispatch routes one validated command.
//
// Realtime commands end in DELIVERED/SUCCEEDED/FAILED/TIMEOUT/NOT_CONNECTED;
// persistent commands either follow the same path when the device is online
// or remain PENDING (synchronous status QUEUED) for delivery on reconnect.
func (r *Router) Dispatch(ctx context.Context, c Command) (Ack, error) {
	now := time.Now().UTC()
	c.CreatedAt = now

	// Lifecycle gate: commands only target ACTIVE devices. Check before
	// recording the command so decommissioned/unclaimed devices never
	// accumulate command rows.
	state, err := r.store.DeviceState(ctx, c.TenantID, c.DeviceID)
	if err != nil {
		return Ack{}, err
	}
	if state != DeviceStateActive {
		return Ack{}, ErrDeviceInactive
	}

	// Every command is recorded first so the chain stays auditable even if
	// dispatch infrastructure fails afterwards.
	expiresAt := now.Add(r.tune.PersistentTTL)
	if err := r.store.Insert(ctx, c, StatusPending, expiresAt); err != nil {
		return Ack{}, err
	}

	// Add-on protocol gate: when the device is served by an optional
	// module, fail fast if that module is absent, disabled or stale
	// instead of waiting for dispatch timeouts.
	protocol, err := r.store.DeviceProtocol(ctx, c.TenantID, c.DeviceID)
	if err != nil {
		return Ack{}, fmt.Errorf("resolve device protocol: %w", err)
	}
	if _, isAddOn := addOnProtocols[protocol]; isAddOn {
		online := false
		if r.modules != nil {
			online, err = r.modules.IsOnline(ctx, protocol)
			if err != nil {
				return Ack{}, fmt.Errorf("check module state: %w", err)
			}
		}
		if !online {
			if c.Timeliness == TimelinessRealtime {
				return r.finish(ctx, c, Ack{
					ID:     c.ID,
					Status: StatusModuleOffline,
					Error:  "protocol module is offline",
				})
			}
			// Persistent commands stay queued; the module or device
			// coming online triggers the claim path as usual.
			return Ack{ID: c.ID, Status: StatusQueued}, nil
		}
	}

	location, err := r.locator.Lookup(ctx, presence.Identity{
		TenantID: c.TenantID,
		DeviceID: c.DeviceID,
	})
	if err != nil {
		return Ack{}, fmt.Errorf("presence lookup: %w", err)
	}

	if !location.Found {
		if c.Timeliness == TimelinessRealtime {
			return r.finish(ctx, c, Ack{
				ID:     c.ID,
				Status: StatusNotConnected,
				Error:  "device not connected",
			})
		}
		return Ack{ID: c.ID, Status: StatusQueued}, nil
	}

	ack, err := r.send(ctx, c, location)
	if err != nil {
		return Ack{}, err
	}
	return r.finish(ctx, c, ack)
}

// send publishes the dispatch request to the owning ingress and waits for
// its ack. The wait is the request-response timeout for that interaction and
// the shorter delivery timeout for one-way commands.
func (r *Router) send(ctx context.Context, c Command, location presence.Location) (Ack, error) {
	wire := toWireCommand(c, location.GatewayID)
	raw, err := proto.Marshal(wire)
	if err != nil {
		return Ack{}, fmt.Errorf("marshal command: %w", err)
	}

	wait := r.tune.DeliveryTimeout
	if c.Interaction == InteractionRequestResponse && c.Timeout > 0 {
		wait = c.Timeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	msg, err := r.nc.RequestMsgWithContext(reqCtx, &nats.Msg{
		Subject: dispatchSubject(location.IngressID),
		Data:    raw,
	})
	if err != nil {
		return r.dispatchError(c.ID, err), nil
	}

	var wireAck v1.CommandAck
	if err := proto.Unmarshal(msg.Data, &wireAck); err != nil {
		return Ack{
			ID:     c.ID,
			Status: StatusFailed,
			Error:  "invalid dispatch ack",
		}, nil
	}
	return Ack{
		ID:         wireAck.Id,
		Status:     wireAck.Status,
		Response:   wireAck.Response,
		Error:      wireAck.Error,
		FinishedAt: time.Now().UTC(),
	}, nil
}

// toWireCommand builds the Core NATS dispatch protobuf. A non-empty gateway
// id (taken from presence) tells the owning ingress to relay to the gateway
// session rather than the direct-device command topic.
func toWireCommand(c Command, gatewayID string) *v1.Command {
	return &v1.Command{
		Id:          c.ID,
		TenantId:    c.TenantID,
		DeviceId:    c.DeviceID,
		Name:        c.Name,
		Params:      c.Params,
		Interaction: c.Interaction,
		Timeliness:  c.Timeliness,
		TimeoutMs:   c.Timeout.Milliseconds(),
		CreatedAtMs: c.CreatedAt.UnixMilli(),
		GatewayId:   gatewayID,
		Direction:   c.Direction,
	}
}

// dispatchError maps a Core NATS request failure to a command status.
func (r *Router) dispatchError(id string, err error) Ack {
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nats.ErrTimeout) {
		return Ack{ID: id, Status: StatusTimeout, Error: "response timeout"}
	}
	if errors.Is(err, nats.ErrNoResponders) {
		return Ack{ID: id, Status: StatusNotConnected, Error: "ingress unavailable"}
	}
	return Ack{ID: id, Status: StatusFailed, Error: err.Error()}
}

// finish records the terminal status and returns the ack unchanged.
func (r *Router) finish(ctx context.Context, c Command, ack Ack) (Ack, error) {
	ack.FinishedAt = time.Now().UTC()
	if err := r.store.Complete(ctx, c.ID, ack.Status, ack.Response, ack.Error); err != nil {
		slog.Error("persist command completion failed",
			"command", c.ID, "error", err)
	}
	r.metrics.Observe(c.TenantID, ack.Status, time.Since(c.CreatedAt))
	return ack, nil
}
