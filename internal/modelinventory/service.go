// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package modelinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// AlarmRaiser opens and clears the deduplicated model/runtime alarms. It is
// satisfied by *alarm.Service; a nil raiser disables alarming.
type AlarmRaiser interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// Store is the persistence surface the service needs, satisfied by *PGStore.
type Store interface {
	GatewayExists(ctx context.Context, tenantID, gatewayID string) (bool, error)
	Reconcile(ctx context.Context, tenantID, gatewayID string, reported []Input, at time.Time) (ReconcileResult, error)
	ListByGateway(ctx context.Context, tenantID, gatewayID string) ([]Model, error)
	ClaimFailedForRaise(ctx context.Context) ([]Model, error)
	MarkStale(ctx context.Context, tenantID, gatewayID string, at time.Time) (bool, error)
	// MarkInventoryStale flips inventory rows stale for every gateway whose
	// models-segment watermark is older than cutoff and returns the distinct
	// gateways that newly flipped (used to raise exactly one stale alarm
	// each, safe to run on every replica).
	MarkInventoryStale(ctx context.Context, cutoff, at time.Time) ([]GatewayRef, error)
	ListRuntimeBlocks(ctx context.Context) ([]RuntimeRow, error)
}

// DefaultSweepInterval is how often failed-model claims and runtime staleness
// are evaluated. Runtime freshness is on the order of probe intervals; 30s
// keeps detection near-realtime while bounding shadow reads.
const DefaultSweepInterval = 30 * time.Second

// Service reconciles the manifest models segment, serves the console view,
// and owns the model/runtime stale watchdog (ADR-0039 §3.5). It never
// initiates inference and never interprets labels or family semantics.
type Service struct {
	nc            *nats.Conn
	store         Store
	alarms        AlarmRaiser
	sweepInterval time.Duration
	log           *slog.Logger
}

// NewService constructs a Service.
func NewService(nc *nats.Conn, store Store, alarms AlarmRaiser,
	sweepInterval time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if sweepInterval <= 0 {
		sweepInterval = DefaultSweepInterval
	}
	return &Service{
		nc:            nc,
		store:         store,
		alarms:        alarms,
		sweepInterval: sweepInterval,
		log:           log,
	}
}

// Reconcile applies one authoritative full-replacement models segment. It is
// invoked from the MQTT manifest handler only when the field is present (an
// absent segment skips reconciliation entirely). Alarm recovery is derived
// here so a model leaving FAILED or a gateway recovering from staleness
// auto-clears its ADR-0019 event.
func (s *Service) Reconcile(ctx context.Context, tenantID, gatewayID string,
	reported []Input, at time.Time) (ReconcileResult, error) {
	exists, err := s.store.GatewayExists(ctx, tenantID, gatewayID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !exists {
		return ReconcileResult{}, ErrGatewayNotFound
	}
	res, err := s.store.Reconcile(ctx, tenantID, gatewayID, reported, at)
	if err != nil {
		return res, err
	}

	// Auto-clear FAILED alarms for models that recovered or were pruned.
	for i := range res.ClearedFailed {
		m := res.ClearedFailed[i]
		s.clearAlarm(ctx, m.TenantID, m.GatewayID,
			DedupeModelFailedKey(m.GatewayID, m.ModelID, m.Version))
	}
	// A successful full report after a stale gap clears the per-gateway
	// inventory-stale alarm.
	if res.WasStale {
		s.clearAlarm(ctx, tenantID, gatewayID, DedupeModelStaleKey(gatewayID))
		s.publish(EventStale, tenantID, gatewayID)
	}
	if res.Changed() {
		s.publish(EventInventory, tenantID, gatewayID)
	}
	return res, nil
}

// ListForGateway returns the console projection of one gateway's inventory.
func (s *Service) ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]Model, error) {
	return s.store.ListByGateway(ctx, tenantID, gatewayID)
}

// OnManifestLeaseExpired is the manifest.LeaseExpirySink hook: when the
// shared gw-manifest lease expires the model inventory is marked stale (kept,
// not pruned) and one per-gateway stale alarm is opened.
func (s *Service) OnManifestLeaseExpired(ctx context.Context, tenantID, gatewayID string) {
	at := time.Now().UTC()
	newlyStale, err := s.store.MarkStale(ctx, tenantID, gatewayID, at)
	if err != nil {
		s.log.Error("mark model inventory stale failed",
			"gateway", gatewayID, "error", err)
		return
	}
	if !newlyStale {
		return
	}
	s.raiseStale(ctx, tenantID, gatewayID, StaleReasonLeaseExpired, at)
}

// raiseStale opens the per-gateway inventory-stale alarm for one reason and
// notifies the console. The dedupe key is shared by both stale reasons so a
// gateway can carry at most one open GATEWAY_MODEL_STALE at a time.
func (s *Service) raiseStale(ctx context.Context, tenantID, gatewayID, reason string, at time.Time) {
	if s.alarms != nil {
		if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  tenantID,
			DeviceID:  gatewayID,
			AlarmType: AlarmTypeModelStale,
			Severity:  alarm.SeverityWarning,
			DedupeKey: DedupeModelStaleKey(gatewayID),
			Details: map[string]any{
				"reason": reason,
				"at":     at,
			},
		}); err != nil {
			s.log.Error("raise model stale alarm failed",
				"gateway", gatewayID, "error", err)
		}
	}
	s.publish(EventStale, tenantID, gatewayID)
}

// Run executes the stale watchdog until ctx is canceled. The FAILED-model
// claim is an atomic UPDATE so the ticker may run on every replica without
// double-raising; runtime alarm open/clear is idempotent on its dedupe key.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx, time.Now().UTC())
		}
	}
}

func (s *Service) sweep(ctx context.Context, now time.Time) {
	// Model plane: claim and raise FAILED models once each.
	failed, err := s.store.ClaimFailedForRaise(ctx)
	if err != nil {
		s.log.Error("claim failed models failed", "error", err)
	} else {
		for i := range failed {
			s.raiseModelFailed(ctx, failed[i])
		}
	}

	// Model plane, segment freshness: a negotiated gateway that keeps
	// sending manifest frames but omits the models segment keeps renewing
	// the shared frame lease, so the lease-expiry hook never fires. The
	// per-segment watermark catches that silent omission instead. Gateways
	// that never reported the segment have no watermark row and are inert.
	cutoff := now.Add(-DefaultInventoryStaleThreshold)
	staleGateways, err := s.store.MarkInventoryStale(ctx, cutoff, now)
	if err != nil {
		s.log.Error("mark segment-silent inventory stale failed", "error", err)
	} else {
		for i := range staleGateways {
			g := staleGateways[i]
			s.raiseStale(ctx, g.TenantID, g.GatewayID, StaleReasonSegmentSilent, now)
		}
	}

	// Runtime plane: evaluate the shadow inference block of every gateway
	// that negotiated inference.
	rows, err := s.store.ListRuntimeBlocks(ctx)
	if err != nil {
		s.log.Error("list inference runtime blocks failed", "error", err)
		return
	}
	for _, row := range rows {
		s.evaluateRuntime(ctx, row, now)
	}
}

func (s *Service) raiseModelFailed(ctx context.Context, m Model) {
	if s.alarms == nil {
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  m.TenantID,
		DeviceID:  m.GatewayID,
		AlarmType: AlarmTypeModelFailed,
		Severity:  alarm.SeverityMajor,
		DedupeKey: DedupeModelFailedKey(m.GatewayID, m.ModelID, m.Version),
		Details: map[string]any{
			"model_id": m.ModelID,
			"version":  m.Version,
			"revision": m.Revision,
			"task":     m.Task,
			"backend":  m.Backend,
			"reason":   m.Reason,
		},
	}); err != nil {
		s.log.Error("raise model failed alarm failed",
			"model", fmt.Sprintf("%s/%s", m.ModelID, m.Version), "error", err)
	}
}

// evaluateRuntime opens the runtime alarm for OFFLINE or a stale (silent)
// health block and clears it once the runtime reports a fresh ONLINE/DEGRADED.
func (s *Service) evaluateRuntime(ctx context.Context, row RuntimeRow, now time.Time) {
	if s.alarms == nil {
		return
	}
	snap := parseRuntimeSnapshot(row.Block)
	effective := snap.classifyRuntime(now)
	key := DedupeRuntimeKey(row.GatewayID)

	switch effective {
	case RuntimeOffline, RuntimeUnknown:
		severity := alarm.SeverityWarning // silent block (UNKNOWN/stale)
		if effective == RuntimeOffline {
			severity = alarm.SeverityMajor
		}
		if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  row.TenantID,
			DeviceID:  row.GatewayID,
			AlarmType: AlarmTypeRuntime,
			Severity:  severity,
			DedupeKey: key,
			Details: map[string]any{
				"state":            effective,
				"reported_state":   snap.State,
				"checked_at":       snap.CheckedAt,
				"probe_interval_s": snap.ProbeIntervalS,
				"models_ready":     snap.ModelsReady,
				"models_total":     snap.ModelsTotal,
				"active_bindings":  snap.ActiveBindings,
			},
		}); err != nil {
			s.log.Error("raise inference runtime alarm failed",
				"gateway", row.GatewayID, "error", err)
			return
		}
		s.publish(EventRuntime, row.TenantID, row.GatewayID)
	case RuntimeOnline, RuntimeDegraded:
		if s.clearAlarm(ctx, row.TenantID, row.GatewayID, key) {
			s.publish(EventRuntime, row.TenantID, row.GatewayID)
		}
	}
}

// clearAlarm clears a deduplicated alarm, reporting whether a transition
// happened. A nil raiser or any error reports false so callers skip events.
func (s *Service) clearAlarm(ctx context.Context, tenantID, deviceID, key string) bool {
	if s.alarms == nil {
		return false
	}
	out, err := s.alarms.ClearAlarm(ctx, tenantID, deviceID, key)
	if err != nil {
		s.log.Error("clear model/runtime alarm failed", "key", key, "error", err)
		return false
	}
	// An empty AlarmID means no open alarm changed state.
	return out.AlarmID != ""
}

func (s *Service) publish(kind, tenantID, gatewayID string) {
	if s.nc == nil {
		return
	}
	data, err := json.Marshal(EventFrame{
		Kind: kind, TenantID: tenantID, GatewayID: gatewayID,
	})
	if err != nil {
		s.log.Error("marshal model event", "error", err)
		return
	}
	subject := SubjectEventPrefix + "." + tenantID
	if err := s.nc.Publish(subject, data); err != nil {
		s.log.Error("publish model event", "error", err, "subject", subject)
	}
}
