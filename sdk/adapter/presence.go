// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
)

// Presence KV bucket and event subject names. They must match the presence
// service constants; duplicated here so external module containers do not
// have to import the in-process presence implementation.
const (
	presenceKVTopology = "topology"
	presenceKVBucket   = "presence"
	presenceSubject    = "nl.presence"
)

// PresenceClient is the remote presence boundary used by module containers.
// It maintains the node lease for the module ingress in the topology KV and
// writes device presence keys directly, so the core command router resolves
// module devices through the same lookup path as embedded ingresses.
type PresenceClient struct {
	nc        *nats.Conn
	js        jetstream.JetStream
	ingressID string

	heartbeatInterval time.Duration
	leaseTTL          time.Duration

	topology jetstream.KeyValue
	presence jetstream.KeyValue

	startOnce sync.Once
}

// SetIngressID sets the module ingress identity after enrollment. The
// value comes from the provisioned module so the adapter does not have to
// know it before registration.
func (c *PresenceClient) SetIngressID(ingressID string) {
	c.ingressID = ingressID
}

// PresenceTuning configures the remote client leases.
type PresenceTuning struct {
	// HeartbeatInterval paces node lease refresh.
	HeartbeatInterval time.Duration

	// LeaseTTL bounds the topology key lifetime and sets the KV TTL.
	LeaseTTL time.Duration
}

// NewPresenceClient constructs a remote presence client. Start must run
// before Connect to ensure the KV buckets and the node lease.
func NewPresenceClient(nc *nats.Conn, js jetstream.JetStream, ingressID string, tune PresenceTuning) *PresenceClient {
	return &PresenceClient{
		nc:                nc,
		js:                js,
		ingressID:         ingressID,
		heartbeatInterval: tune.HeartbeatInterval,
		leaseTTL:          tune.LeaseTTL,
	}
}

// Start ensures the KV buckets and begins the node lease heartbeat. It is
// safe to call once; subsequent calls return the startup error.
func (c *PresenceClient) Start(ctx context.Context) error {
	var startErr error
	c.startOnce.Do(func() {
		topology, err := c.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: presenceKVTopology,
			TTL:    c.leaseTTL,
		})
		if err != nil {
			startErr = fmt.Errorf("ensure topology KV: %w", err)
			return
		}
		presence, err := c.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: presenceKVBucket,
		})
		if err != nil {
			startErr = fmt.Errorf("ensure presence KV: %w", err)
			return
		}
		c.topology = topology
		c.presence = presence
	})
	return startErr
}

// Run starts the client and heartbeats the node lease until ctx is done.
func (c *PresenceClient) Run(ctx context.Context) error {
	if err := c.Start(ctx); err != nil {
		return err
	}
	if err := c.heartbeatOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := c.heartbeatOnce(ctx); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// heartbeatOnce refreshes the topology key, resetting its TTL.
func (c *PresenceClient) heartbeatOnce(ctx context.Context) error {
	_, err := c.topology.Put(ctx, c.ingressID,
		[]byte(time.Now().UTC().Format(time.RFC3339)))
	if err != nil {
		return fmt.Errorf("topology heartbeat: %w", err)
	}
	return nil
}

// Connect opens a device session: it writes the presence key with a fresh
// fencing session ID and publishes ONLINE. An existing key is overwritten,
// fencing the previous session.
func (c *PresenceClient) Connect(ctx context.Context, id SessionIdentity) (string, error) {
	sessionID := uuid.NewString()
	now := time.Now().UTC()

	value := &v1.PresenceValue{
		SessionId:   sessionID,
		IngressId:   c.ingressID,
		ConnectedAt: now.UnixMilli(),
	}
	raw, err := proto.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal presence value: %w", err)
	}
	if _, err := c.presence.Put(ctx, presenceKey(id), raw); err != nil {
		return "", fmt.Errorf("put presence: %w", err)
	}
	c.emit(ctx, id, "ONLINE", sessionID, now)
	return sessionID, nil
}

// Disconnect ends a session only when sessionID still fences it, matching
// the in-process presence fencing rules.
func (c *PresenceClient) Disconnect(ctx context.Context, id SessionIdentity, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	entry, err := c.presence.Get(ctx, presenceKey(id))
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("get presence: %w", err)
	}
	var value v1.PresenceValue
	if err := proto.Unmarshal(entry.Value(), &value); err != nil {
		return fmt.Errorf("decode presence: %w", err)
	}
	if value.SessionId != sessionID {
		return nil
	}
	if err := c.presence.Delete(ctx, presenceKey(id)); err != nil {
		return fmt.Errorf("delete presence: %w", err)
	}
	c.emit(ctx, id, "OFFLINE", sessionID, time.Now().UTC())
	return nil
}

// emit publishes one presence boundary event to the fanout subject.
func (c *PresenceClient) emit(ctx context.Context, id SessionIdentity, state, sessionID string, at time.Time) {
	event := map[string]string{
		"time":       at.Format(time.RFC3339),
		"tenant_id":  id.TenantID,
		"device_id":  id.DeviceID,
		"state":      state,
		"reason":     "session",
		"session_id": sessionID,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	_ = c.nc.Publish(presenceSubject+"."+id.TenantID+"."+id.DeviceID, raw)
}

// presenceKey builds the per-device KV key.
func presenceKey(id SessionIdentity) string {
	return id.TenantID + "." + id.DeviceID
}
