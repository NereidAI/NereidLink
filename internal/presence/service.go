// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package presence implements the cascading-lease presence model.
//
// The model keeps two lease layers instead of refreshing every device:
//
//   - Node lease at topology.<ingress-id>: the node refreshes it with a
//     heartbeat. One lease per node keeps refresh load constant.
//   - Device presence at presence.<tenant>.<device>: created when a
//     transport session starts and deleted when it ends, never refreshed
//     per device. A live node lease makes its presence keys valid.
//
// When a node crashes its lease TTL expires; a topology watcher derives
// OFFLINE for the node's devices in rate-limited batches. Session fencing
// rejects late disconnects from superseded sessions.
package presence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
)

// KV bucket and subject identifiers.
const (
	// BucketTopology stores node leases keyed by ingress ID.
	BucketTopology = "topology"

	// BucketPresence stores live device presence keys.
	BucketPresence = "presence"

	// SubjectPresencePrefix is the Core NATS fanout root for events.
	SubjectPresencePrefix = "nl.presence"
)

// State transition reasons attached to boundary records.
const (
	// ReasonSession is a normal transport-level connect or disconnect.
	ReasonSession = "session"

	// ReasonNodeLeaseExpired is a batch offline after a node crash.
	ReasonNodeLeaseExpired = "node_lease_expired"

	// ReasonShutdown is a graceful node shutdown.
	ReasonShutdown = "shutdown"

	// ReasonDecommissioned is an administrative device decommission.
	ReasonDecommissioned = "decommissioned"
)

// Tuning holds lease and batching settings from configuration.
type Tuning struct {
	// JSReplicas is the JetStream Raft factor for the KV buckets (1/3/5).
	JSReplicas int

	// HeartbeatInterval is the node lease refresh interval.
	HeartbeatInterval time.Duration

	// LeaseTTL is the node lease time-to-live.
	LeaseTTL time.Duration

	// OfflineBatchSize bounds one batch during derived offline scans.
	OfflineBatchSize int

	// OfflineBatchInterval paces derived offline deletion.
	OfflineBatchInterval time.Duration

	// PersistBufferSize bounds the boundary persistence queue.
	PersistBufferSize int
}

// Identity identifies the device whose session changed.
type Identity struct {
	// TenantID owns the device.
	TenantID string

	// DeviceID is the device identifier.
	DeviceID string
}

// Location is the resolved live location of a device.
type Location struct {
	// Found reports whether a live presence key exists.
	Found bool

	// IngressID owns the live transport session.
	IngressID string

	// SessionID is the fencing token of the live session.
	SessionID string

	// GatewayID is set when attached through a gateway.
	GatewayID string
}

// Lookup resolves the live ingress location of a device from presence KV.
// It is the read path used by the command router; a missing key yields an
// empty Location with Found=false rather than an error.
func (s *Service) Lookup(ctx context.Context, id Identity) (Location, error) {
	entry, err := s.presence.Get(ctx, presenceKey(id))
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Location{}, nil
		}
		return Location{}, fmt.Errorf("get presence: %w", err)
	}

	var value v1.PresenceValue
	if err := proto.Unmarshal(entry.Value(), &value); err != nil {
		return Location{}, fmt.Errorf("unmarshal presence value: %w", err)
	}
	return Location{
		Found:     true,
		IngressID: value.IngressId,
		SessionID: value.SessionId,
		GatewayID: value.GatewayId,
	}, nil
}

// CountOnline returns the number of live presence keys belonging to one
// tenant. Presence KV keys use "<tenant-id>.<device-id>", so the count is a
// prefix scan rather than a database query.
func (s *Service) CountOnline(ctx context.Context, tenantID string) (int, error) {
	if s.presence == nil {
		return 0, fmt.Errorf("presence KV is not initialized")
	}
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("list presence keys: %w", err)
	}
	prefix := tenantID + "."
	count := 0
	for key := range lister.Keys() {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count, nil
}

// OnlineDevice is one live presence entry of a tenant.
type OnlineDevice struct {
	// DeviceID is the online device identifier.
	DeviceID string

	// IngressID owns the live transport session.
	IngressID string

	// SessionID is the fencing token of the live session.
	SessionID string

	// GatewayID is set when attached through a gateway.
	GatewayID string
}

// ListOnline returns every live presence entry belonging to one tenant.
// Presence KV keys use "<tenant-id>.<device-id>"; it backs the presence
// snapshot served to freshly loaded UI clients, which cannot replay the
// ONLINE event that happened before page load.
func (s *Service) ListOnline(ctx context.Context, tenantID string) ([]OnlineDevice, error) {
	if s.presence == nil {
		return nil, fmt.Errorf("presence KV is not initialized")
	}
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list presence: %w", err)
	}
	prefix := tenantID + "."
	devices := []OnlineDevice{}
	for key := range lister.Keys() {
		deviceID, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		entry, err := s.presence.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			// The key disappeared between ListKeys and Get (a concurrent
			// disconnect). It is not part of the snapshot anymore.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get presence %q: %w", key, err)
		}
		var value v1.PresenceValue
		if err := proto.Unmarshal(entry.Value(), &value); err != nil {
			return nil, fmt.Errorf("unmarshal presence value: %w", err)
		}
		devices = append(devices, OnlineDevice{
			DeviceID:  deviceID,
			IngressID: value.IngressId,
			SessionID: value.SessionId,
			GatewayID: value.GatewayId,
		})
	}
	return devices, nil
}

// OnlineEntry is one live presence entry across all tenants.
type OnlineEntry struct {
	// TenantID owns the device.
	TenantID string
	// DeviceID is the online device identifier.
	DeviceID string
	// ConnectedAt is when the current transport session started; zero when
	// the live value predates the field.
	ConnectedAt time.Time
}

// ListAllOnline returns every live presence entry across all tenants. It is
// a maintenance path for platform-wide batch sweeps (ADR-0014 silence
// detection); per-tenant callers should use ListOnline. Keys that vanish
// between listing and reading are skipped as concurrent disconnects.
func (s *Service) ListAllOnline(ctx context.Context) ([]OnlineEntry, error) {
	if s.presence == nil {
		return nil, fmt.Errorf("presence KV is not initialized")
	}
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list presence: %w", err)
	}
	entries := []OnlineEntry{}
	for key := range lister.Keys() {
		tenantID, deviceID, ok := splitPresenceKey(key)
		if !ok {
			continue
		}
		entry, err := s.presence.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get presence %q: %w", key, err)
		}
		var value v1.PresenceValue
		if err := proto.Unmarshal(entry.Value(), &value); err != nil {
			return nil, fmt.Errorf("unmarshal presence value: %w", err)
		}
		var connectedAt time.Time
		if value.ConnectedAt > 0 {
			connectedAt = time.UnixMilli(value.ConnectedAt).UTC()
		}
		entries = append(entries, OnlineEntry{
			TenantID:    tenantID,
			DeviceID:    deviceID,
			ConnectedAt: connectedAt,
		})
	}
	return entries, nil
}

// Event is the JSON payload published to Core NATS presence subjects.
type Event struct {
	// Time is the boundary time as RFC3339 UTC.
	Time string `json:"time"`

	// TenantID owns the affected device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the affected device.
	DeviceID string `json:"device_id"`

	// GatewayID is set for gateway-attached sessions.
	GatewayID string `json:"gateway_id,omitempty"`

	// State is ONLINE or OFFLINE.
	State string `json:"state"`

	// Reason explains the boundary.
	Reason string `json:"reason"`

	// SessionID is the fencing token.
	SessionID string `json:"session_id"`
}

// persistEntry is one queued history row.
type persistEntry struct {
	ts        time.Time
	tenantID  string
	deviceID  string
	gatewayID string
	state     string
	reason    string
	sessionID string
}

// session tracks one locally owned session for fenced shutdown.
type session struct {
	identity  Identity
	sessionID string
	gatewayID string
}

// Service owns the presence KV buckets, leases and persistence.
type Service struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	topology jetstream.KeyValue
	presence jetstream.KeyValue
	pool     *pgxpool.Pool

	ingressID string
	tune      Tuning

	mu       sync.Mutex
	sessions map[string]session

	persistCh chan persistEntry
}

// NewService constructs a presence service for ingressID.
func NewService(nc *nats.Conn, js jetstream.JetStream, pool *pgxpool.Pool, ingressID string, tune Tuning) *Service {
	return &Service{
		nc:        nc,
		js:        js,
		pool:      pool,
		ingressID: ingressID,
		tune:      tune,
		sessions:  map[string]session{},
		persistCh: make(chan persistEntry, tune.PersistBufferSize),
	}
}

// PersistQueueWatermark reports the boundary-persistence queue occupancy as
// current/cap. It backs the ADR-0025 capacity watermark metrics.
func (s *Service) PersistQueueWatermark() (current, limit int) {
	return len(s.persistCh), cap(s.persistCh)
}

// Run prepares buckets, starts the heartbeat, topology watcher and
// persistence worker, and blocks until ctx is canceled.
func (s *Service) Run(ctx context.Context) error {
	topology, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: BucketTopology,
		// Key-level TTL is not supported by KV; the lease lifetime is set
		// on the bucket and every heartbeat Put resets the timer.
		TTL:      s.tune.LeaseTTL,
		Replicas: jsReplicas(s.tune.JSReplicas),
	})
	if err != nil {
		return fmt.Errorf("ensure topology KV: %w", err)
	}
	s.topology = topology

	presence, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   BucketPresence,
		Replicas: jsReplicas(s.tune.JSReplicas),
	})
	if err != nil {
		return fmt.Errorf("ensure presence KV: %w", err)
	}
	s.presence = presence

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return s.heartbeat(gctx) })
	g.Go(func() error { return s.peerSweep(gctx) })
	g.Go(func() error { return s.watchTopology(gctx) })
	g.Go(func() error { return s.persistLoop(gctx) })
	return g.Wait()
}

// heartbeat writes the node lease immediately and refreshes it on every
// tick. A missed refresh after LeaseTTL lets the topology watcher derive
// offlines on surviving nodes.
func (s *Service) heartbeat(ctx context.Context) error {
	refresh := func() error {
		_, err := s.topology.Put(ctx, s.ingressID, []byte(time.Now().UTC().Format(time.RFC3339)))
		if err != nil {
			return err
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}

	ticker := time.NewTicker(s.tune.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := refresh(); err != nil {
				slog.Warn("topology heartbeat failed", "error", err)
			}
		}
	}
}

// presenceKey builds the device presence key.
func presenceKey(id Identity) string {
	return fmt.Sprintf("%s.%s", id.TenantID, id.DeviceID)
}

// presenceSubject builds the Core NATS fanout subject.
func presenceSubject(id Identity) string {
	return fmt.Sprintf("%s.%s.%s", SubjectPresencePrefix, id.TenantID, id.DeviceID)
}

// Connect starts a new transport session: it writes the presence key with a
// fresh fencing session ID, publishes ONLINE and queues history. ingressID
// identifies the transport-specific dispatch subject so commands route to
// the correct ingress relay (MQTT and HTTP relays use distinct IDs in the
// same node process). An existing key is overwritten, which fences the
// previous session; a late disconnect from that session is rejected.
func (s *Service) Connect(ctx context.Context, id Identity, ingressID, gatewayID string) (string, error) {
	sessionID := uuidString()
	now := time.Now().UTC()

	if ingressID == "" {
		ingressID = s.ingressID
	}
	value := &v1.PresenceValue{
		SessionId:   sessionID,
		IngressId:   ingressID,
		GatewayId:   gatewayID,
		ConnectedAt: now.UnixMilli(),
	}
	raw, err := proto.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal presence value: %w", err)
	}

	key := presenceKey(id)
	if _, err := s.presence.Put(ctx, key, raw); err != nil {
		return "", fmt.Errorf("put presence: %w", err)
	}

	s.mu.Lock()
	s.sessions[key] = session{identity: id, sessionID: sessionID, gatewayID: gatewayID}
	s.mu.Unlock()

	s.emit(ctx, id, gatewayID, "ONLINE", ReasonSession, sessionID, now)
	return sessionID, nil
}

// Disconnect ends a session only when sessionID matches the stored fencing
// token. This prevents a delayed disconnect from an old session from
// removing the presence of a reconnected device.
func (s *Service) Disconnect(ctx context.Context, id Identity, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	key := presenceKey(id)
	entry, err := s.presence.Get(ctx, key)
	if err != nil {
		// The key is already gone, commonly because a batch offline ran.
		s.forget(key, sessionID)
		return nil
	}

	var value v1.PresenceValue
	if err := proto.Unmarshal(entry.Value(), &value); err != nil {
		return fmt.Errorf("decode presence: %w", err)
	}
	if value.SessionId != sessionID {
		// Fenced by a newer session; the old disconnect is ignored.
		s.forget(key, sessionID)
		return nil
	}

	if err := s.presence.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete presence: %w", err)
	}

	s.forget(key, sessionID)
	s.emit(ctx, id, value.GatewayId, "OFFLINE", ReasonSession, sessionID, time.Now().UTC())
	return nil
}

// DisconnectGateway ends the gateway session and cascades OFFLINE to every
// sub-device whose presence is attached to it. It is the transport teardown
// path for gateway connections (ADR-0010): one physical gateway disconnect
// must not leave its sub-devices showing ONLINE.
func (s *Service) DisconnectGateway(ctx context.Context, tenantID, gatewayID string) error {
	if gatewayID == "" {
		return nil
	}
	if err := s.cascadeGatewayOffline(ctx, tenantID, gatewayID); err != nil {
		return err
	}
	key := presenceKey(Identity{TenantID: tenantID, DeviceID: gatewayID})
	if err := s.presence.Delete(ctx, key); err != nil &&
		!errors.Is(err, jetstream.ErrKeyNotFound) {
		return err
	}
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
	s.emit(ctx, Identity{TenantID: tenantID, DeviceID: gatewayID}, "",
		"OFFLINE", ReasonSession, "", time.Now().UTC())
	return nil
}

// cascadeGatewayOffline removes presence keys whose gateway binding matches
// gatewayID and emits one OFFLINE boundary per sub-device. The scan is the
// same trade-off as deriveOffline: it runs only on gateway teardown, off the
// per-frame hot path.
func (s *Service) cascadeGatewayOffline(ctx context.Context, tenantID, gatewayID string) error {
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return err
	}
	batch := make([]Identity, 0, s.tune.OfflineBatchSize)
	for key := range lister.Keys() {
		if !strings.HasPrefix(key, tenantID+".") {
			continue
		}
		entry, err := s.presence.Get(ctx, key)
		if err != nil {
			continue
		}
		var value v1.PresenceValue
		if err := proto.Unmarshal(entry.Value(), &value); err != nil {
			continue
		}
		if value.GatewayId != gatewayID {
			continue
		}
		deviceID, ok := strings.CutPrefix(key, tenantID+".")
		if !ok || deviceID == gatewayID {
			continue
		}
		batch = append(batch, Identity{TenantID: tenantID, DeviceID: deviceID})
	}
	return s.offlineBatch(ctx, batch, ReasonSession)
}

// ForceRemove deletes a device presence entry regardless of which session
// owns it and emits one OFFLINE boundary. It is the administrative path
// used by device decommissioning, where the lifecycle transition, not the
// transport session, drives the state change. A missing key is a no-op.
func (s *Service) ForceRemove(ctx context.Context, id Identity, reason string) error {
	key := presenceKey(id)
	if err := s.presence.Delete(ctx, key); err != nil &&
		!errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("delete presence: %w", err)
	}

	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()

	s.emit(ctx, id, "", "OFFLINE", reason, "", time.Now().UTC())
	return nil
}

// forget removes the locally tracked session when its token matches.
func (s *Service) forget(key, sessionID string) {
	s.mu.Lock()
	if current, ok := s.sessions[key]; ok && current.sessionID == sessionID {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
}

// emit publishes the boundary to Core NATS for real-time fanout and queues
// one history row. Core publish failures are logged but must not block the
// transport path.
func (s *Service) emit(ctx context.Context, id Identity, gatewayID, state, reason, sessionID string, at time.Time) {
	event := Event{
		Time:      at.Format(time.RFC3339),
		TenantID:  id.TenantID,
		DeviceID:  id.DeviceID,
		GatewayID: gatewayID,
		State:     state,
		Reason:    reason,
		SessionID: sessionID,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		slog.Error("presence event marshal failed", "error", err)
	} else if err := s.nc.Publish(presenceSubject(id), raw); err != nil {
		slog.Error("presence event publish failed", "error", err)
	}

	select {
	case s.persistCh <- persistEntry{
		ts:        at,
		tenantID:  id.TenantID,
		deviceID:  id.DeviceID,
		gatewayID: gatewayID,
		state:     state,
		reason:    reason,
		sessionID: sessionID,
	}:
	default:
		slog.Warn("presence persist queue full, history entry dropped")
	}
}

// peerSweep actively detects dead peers.
//
// Natural KV TTL expiry delivers neither a watch event nor a tombstone: the
// key simply disappears after LeaseTTL. The sweep therefore remembers the
// last heartbeat time observed for each peer. On every sweep live keys
// refresh that timestamp; a tracked peer missing from the listing whose last
// heartbeat is older than LeaseTTL has crashed, and its devices are derived
// offline exactly once.
func (s *Service) peerSweep(ctx context.Context) error {
	lastBeat := map[string]time.Time{}
	derived := map[string]struct{}{}

	ticker := time.NewTicker(s.tune.HeartbeatInterval)
	defer ticker.Stop()

	sweep := func() {
		lister, err := s.topology.ListKeys(ctx)
		if err != nil {
			slog.Warn("topology key list failed", "error", err)
			return
		}

		seen := map[string]struct{}{}
		for key := range lister.Keys() {
			if key == s.ingressID {
				continue
			}
			entry, err := s.topology.Get(ctx, key)
			if err != nil {
				continue
			}
			rawBeat := entry.Value()
			beat, err := time.Parse(time.RFC3339, string(rawBeat))
			if err != nil {
				continue
			}
			seen[key] = struct{}{}

			if previous, ok := lastBeat[key]; !ok || beat.After(previous) {
				lastBeat[key] = beat
				delete(derived, key)
			}
		}

		now := time.Now().UTC()
		for key, beat := range lastBeat {
			if _, present := seen[key]; present {
				continue
			}
			if !now.After(beat.Add(s.tune.LeaseTTL)) {
				continue
			}
			if _, already := derived[key]; already {
				continue
			}
			derived[key] = struct{}{}
			slog.Warn("peer heartbeat expired, deriving offline",
				"ingress", key)
			if err := s.deriveOffline(ctx, key, ReasonNodeLeaseExpired); err != nil {
				slog.Error("derive offline failed",
					"ingress", key, "error", err)
			}
		}
	}

	sweep()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sweep()
		}
	}
}

// watchTopology observes explicit node lease deletions. It complements
// peerSweep for cases where a lease is removed explicitly rather than
// expiring silently through KV TTL.
func (s *Service) watchTopology(ctx context.Context) error {
	watcher, err := s.topology.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("watch topology: %w", err)
	}
	defer func() { _ = watcher.Stop() }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case entry := <-watcher.Updates():
			if entry == nil {
				continue
			}
			if entry.Operation() != jetstream.KeyValueDelete &&
				entry.Operation() != jetstream.KeyValuePurge {
				continue
			}
			deadIngress := entry.Key()
			if deadIngress == s.ingressID {
				continue
			}
			slog.Warn("node lease expired, deriving offline", "ingress", deadIngress)
			if err := s.deriveOffline(ctx, deadIngress, ReasonNodeLeaseExpired); err != nil {
				slog.Error("derive offline failed", "ingress", deadIngress, "error", err)
			}
		}
	}
}

// deriveOffline scans presence keys and removes entries owned by the dead
// ingress, pacing deletion in bounded batches. The full scan is the known
// trade-off documented for this path; it runs off the hot path and only on
// the rare lease-expiry event.
func (s *Service) deriveOffline(ctx context.Context, ingressID, reason string) error {
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return err
	}

	batch := make([]Identity, 0, s.tune.OfflineBatchSize)
	for key := range lister.Keys() {
		entry, err := s.presence.Get(ctx, key)
		if err != nil {
			continue
		}
		var value v1.PresenceValue
		if err := proto.Unmarshal(entry.Value(), &value); err != nil {
			continue
		}
		if value.IngressId != ingressID {
			continue
		}

		tenantID, deviceID, ok := splitPresenceKey(key)
		if !ok {
			continue
		}
		batch = append(batch, Identity{TenantID: tenantID, DeviceID: deviceID})
		if len(batch) == s.tune.OfflineBatchSize {
			if err := s.offlineBatch(ctx, batch, reason); err != nil {
				return err
			}
			batch = batch[:0]
			timer := time.NewTimer(s.tune.OfflineBatchInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
	return s.offlineBatch(ctx, batch, reason)
}

// offlineBatch deletes one bounded batch of presence keys and emits the
// corresponding OFFLINE boundaries. Gateway-attached entries keep their
// gateway identifier on the OFFLINE boundary.
func (s *Service) offlineBatch(ctx context.Context, batch []Identity, reason string) error {
	if len(batch) == 0 {
		return nil
	}
	for _, id := range batch {
		gatewayID := s.gatewayFor(id)
		if err := s.presence.Delete(ctx, presenceKey(id)); err != nil {
			slog.Warn("batch presence delete failed", "device", id.DeviceID, "error", err)
		}
		s.emit(ctx, id, gatewayID, "OFFLINE", reason, "", time.Now().UTC())
	}
	return nil
}

// gatewayFor reads the persisted gateway binding of one device, used to keep
// derived OFFLINE boundaries informative. A failed read yields an empty
// gateway; the boundary still records the transition.
func (s *Service) gatewayFor(id Identity) string {
	entry, err := s.presence.Get(context.Background(), presenceKey(id))
	if err != nil {
		return ""
	}
	var value v1.PresenceValue
	if err := proto.Unmarshal(entry.Value(), &value); err != nil {
		return ""
	}
	return value.GatewayId
}

// Shutdown performs graceful node shutdown: it ends every locally owned
// session (fenced), then removes the node lease. Because all owned presence
// keys are already deleted, lease removal produces no derived offlines.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	sessions := make([]session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()

	for _, current := range sessions {
		if err := s.Disconnect(ctx, current.identity, current.sessionID); err != nil {
			slog.Warn("shutdown disconnect failed", "device", current.identity.DeviceID, "error", err)
		}
	}

	if s.topology != nil {
		if err := s.topology.Delete(ctx, s.ingressID); err != nil {
			return fmt.Errorf("delete node lease: %w", err)
		}
	}
	return nil
}

// persistLoop batches boundary rows into ts_presence so presence history is
// durable without making the transport path wait on the database.
func (s *Service) persistLoop(ctx context.Context) error {
	ticker := time.NewTicker(persistFlushInterval)
	defer ticker.Stop()

	buffer := make([]persistEntry, 0, persistBatchSize)

	flushWith := func(execCtx context.Context) error {
		if len(buffer) == 0 {
			return nil
		}
		for _, entry := range buffer {
			if _, err := s.pool.Exec(execCtx,
				`INSERT INTO ts_presence
				 (time, tenant_id, device_id, gateway_id, state, reason, session_id)
				 VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7)`,
				entry.ts, entry.tenantID, entry.deviceID, entry.gatewayID,
				entry.state, entry.reason, entry.sessionID); err != nil {
				return err
			}
		}
		buffer = buffer[:0]
		return nil
	}

	flush := func() error { return flushWith(ctx) }

	for {
		select {
		case entry := <-s.persistCh:
			buffer = append(buffer, entry)
			if len(buffer) >= persistBatchSize {
				if err := flush(); err != nil {
					slog.Error("presence persist failed", "error", err)
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				slog.Error("presence persist failed", "error", err)
			}
		case <-ctx.Done():
			// The final flush must survive request-context cancellation,
			// otherwise shutdown would lose queued history boundaries.
			return flushWith(context.Background())
		}
	}
}

// jsReplicas normalizes the configured JetStream Raft factor: unset/zero
// means the single-node default.
func jsReplicas(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
