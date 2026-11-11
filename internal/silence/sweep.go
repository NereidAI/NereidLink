// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package silence implements the ADR-0014 online-but-silent detector.
//
// A connected device that stops reporting telemetry is a business fault
// distinct from being OFFLINE: the transport session is alive but no data
// arrives. Rather than arming one timer per device, a periodic sweep joins a
// cheap per-device last-telemetry watermark (device.last_telemetry_at,
// maintained by the telemetry writer) against the expected reporting period
// declared on the profile descriptor, and intersects the result with the live
// presence set from NATS KV.
//
// Desired alarm state is reconciled from persistent inputs on every run, so
// the sweeper holds no per-device memory and survives restarts: a device that
// is both online and overdue gets one ONLINE_BUT_SILENT alarm; telemetry
// resuming, the session dropping (OFFLINE), the profile losing its expected
// period, or the device being decommissioned clears it. Raise/clear calls
// happen only on transitions, never per cycle.
package silence

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/presence"
)

// AlarmType classifies an online-but-silent device alarm.
const AlarmType = "ONLINE_BUT_SILENT"

// dedupePrefix names the open alarm per device (per-tenant dedupe index).
const dedupePrefix = "telemetry-silence:"

// defaultTolerance is used when a descriptor carries no multiplier; it
// mirrors the database column default.
const defaultTolerance = 3.0

// PresenceReader enumerates live device sessions. *presence.Service
// satisfies it.
type PresenceReader interface {
	ListAllOnline(ctx context.Context) ([]presence.OnlineEntry, error)
}

// AlarmSink is the alarm surface reconciled by the sweep. *alarm.Service
// satisfies it.
type AlarmSink interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
	ListOpenByType(ctx context.Context, alarmType string) ([]alarm.OpenRef, error)
}

// Sweeper periodically reconciles online-but-silent alarms.
type Sweeper struct {
	pool    *pgxpool.Pool
	online  PresenceReader
	alarms  AlarmSink
	log     *slog.Logger
	nowFunc func() time.Time
}

// New constructs a sweeper.
func New(pool *pgxpool.Pool, online PresenceReader, alarms AlarmSink, log *slog.Logger) *Sweeper {
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{
		pool:    pool,
		online:  online,
		alarms:  alarms,
		log:     log,
		nowFunc: func() time.Time { return time.Now().UTC() },
	}
}

// Run executes one sweep immediately and then on the given interval until
// ctx is canceled.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	if err := s.Sweep(ctx); err != nil {
		s.log.Warn("silence sweep failed", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Sweep(ctx); err != nil {
				s.log.Warn("silence sweep failed", "error", err)
			}
		}
	}
}

// candidate is one monitored device with its reporting deadline inputs.
type candidate struct {
	tenantID      string
	deviceID      string
	createdAt     time.Time
	lastTelemetry *time.Time
	periodSeconds int
	tolerance     float64
}

// key identifies a device across tenants.
type key struct {
	tenant string
	device string
}

func deviceKey(tenant, device string) key { return key{tenant: tenant, device: device} }

func dedupeKey(deviceID string) string { return dedupePrefix + deviceID }

// Sweep runs one reconciliation pass.
func (s *Sweeper) Sweep(ctx context.Context) error {
	now := s.nowFunc()

	cands, err := s.loadCandidates(ctx)
	if err != nil {
		return err
	}
	live, err := s.online.ListAllOnline(ctx)
	if err != nil {
		return fmt.Errorf("list online devices: %w", err)
	}
	open, err := s.alarms.ListOpenByType(ctx, AlarmType)
	if err != nil {
		return fmt.Errorf("list open silence alarms: %w", err)
	}

	connected := make(map[key]time.Time, len(live))
	for _, e := range live {
		connected[deviceKey(e.TenantID, e.DeviceID)] = e.ConnectedAt
	}

	// Desired silence: online AND overdue. Offline or fresh devices are not
	// silent even when a profile declares a period.
	desired := map[key]candidate{}
	for _, c := range cands {
		k := deviceKey(c.tenantID, c.deviceID)
		connectedAt, isOnline := connected[k]
		if !isOnline {
			continue
		}
		if overdue(now, c, connectedAt) {
			desired[k] = c
		}
	}

	openByKey := make(map[key]alarm.OpenRef, len(open))
	for _, ref := range open {
		openByKey[deviceKey(ref.TenantID, ref.DeviceID)] = ref
	}

	toRaise, toClear := reconcile(desired, openByKey)

	for _, k := range toRaise {
		c := desired[k]
		if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  k.tenant,
			DeviceID:  k.device,
			AlarmType: AlarmType,
			Severity:  alarm.SeverityWarning,
			DedupeKey: dedupeKey(k.device),
			Details:   raiseDetails(now, c, connected[k]),
		}); err != nil {
			s.log.Warn("raise online-but-silent alarm failed",
				"device", k.device, "error", err)
		}
	}
	for _, ref := range toClear {
		k := deviceKey(ref.TenantID, ref.DeviceID)
		if _, err := s.alarms.ClearAlarm(ctx, ref.TenantID, ref.DeviceID, ref.DedupeKey); err != nil {
			s.log.Warn("clear online-but-silent alarm failed",
				"device", k.device, "error", err)
		}
	}
	if len(toRaise) > 0 || len(toClear) > 0 {
		s.log.Info("silence sweep reconciled",
			"raised", len(toRaise), "cleared", len(toClear))
	}
	return nil
}

// loadCandidates returns ACTIVE devices that reference at least one
// monitored descriptor. DISTINCT ON picks the shortest expected period on a
// profile; its multiplier sets the deadline.
func (s *Sweeper) loadCandidates(ctx context.Context) ([]candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (d.id)
		       d.tenant_id::text, d.id::text, d.created_at, d.last_telemetry_at,
		       md.expected_period_seconds, md.tolerance_multiplier
		  FROM device d
		  JOIN measurement_descriptor md ON md.profile_id = d.profile_id
		 WHERE d.state = 'ACTIVE'
		   AND md.expected_period_seconds IS NOT NULL
		   AND md.expected_period_seconds > 0
		 ORDER BY d.id, md.expected_period_seconds ASC`)
	if err != nil {
		return nil, fmt.Errorf("load silence candidates: %w", err)
	}
	defer rows.Close()

	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.tenantID, &c.deviceID, &c.createdAt,
			&c.lastTelemetry, &c.periodSeconds, &c.tolerance); err != nil {
			return nil, fmt.Errorf("scan silence candidate: %w", err)
		}
		if c.tolerance < 1 {
			c.tolerance = defaultTolerance
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// lastSeen picks the reference time for the deadline: the latest stored
// point, otherwise the current connection's start, otherwise the device
// creation time. The fallbacks keep a freshly connected device that has
// never reported from alarming until its own deadline elapses.
func lastSeen(c candidate, connectedAt time.Time) time.Time {
	if c.lastTelemetry != nil {
		return *c.lastTelemetry
	}
	if !connectedAt.IsZero() {
		return connectedAt
	}
	return c.createdAt
}

// deadline returns the allowed silence duration for one candidate.
func deadline(c candidate) time.Duration {
	mult := c.tolerance
	if mult < 1 {
		mult = defaultTolerance
	}
	return time.Duration(float64(c.periodSeconds) * mult * float64(time.Second))
}

// overdue reports whether an online device has been silent longer than its
// tolerated deadline.
func overdue(now time.Time, c candidate, connectedAt time.Time) bool {
	return now.Sub(lastSeen(c, connectedAt)) > deadline(c)
}

// reconcile computes the alarm transitions that move the open set onto the
// desired set. It is pure so the transition logic is unit-testable without
// a database or KV.
func reconcile(desired map[key]candidate, open map[key]alarm.OpenRef) (toRaise []key, toClear []alarm.OpenRef) {
	for k := range desired {
		if _, exists := open[k]; !exists {
			toRaise = append(toRaise, k)
		}
	}
	for k, ref := range open {
		if _, still := desired[k]; !still {
			toClear = append(toClear, ref)
		}
	}
	return toRaise, toClear
}

func raiseDetails(now time.Time, c candidate, connectedAt time.Time) map[string]any {
	last := lastSeen(c, connectedAt)
	return map[string]any{
		"expected_period_seconds": c.periodSeconds,
		"deadline_seconds":        int64(deadline(c).Seconds()),
		"silent_since":            last.UTC().Format(time.RFC3339),
		"silent_for_seconds":      int64(now.Sub(last).Seconds()),
		"detected_at":             now.UTC().Format(time.RFC3339),
	}
}
