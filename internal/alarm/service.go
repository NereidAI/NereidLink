// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements alarm lifecycle persistence and real-time event
// publication on Core NATS (ADR-0019).

package alarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// Service owns alarm persistence and event publication.
type Service struct {
	pool *pgxpool.Pool
	nc   *nats.Conn
}

// NewService constructs the alarm service.
func NewService(pool *pgxpool.Pool, nc *nats.Conn) *Service {
	return &Service{pool: pool, nc: nc}
}

// CreateAlarm opens a new alarm or updates the live alarm sharing its dedupe
// key. The upsert uses index inference against the partial unique index on
// live alarms. A higher incoming severity is an escalation.
func (s *Service) CreateAlarm(ctx context.Context, in CreateInput) (Outcome, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	details := in.Details
	if details == nil {
		details = map[string]any{}
	}

	// severityRank turns a severity into an ordered integer inline.
	row := s.pool.QueryRow(ctx,
		`INSERT INTO alarm
		   (id, tenant_id, device_id, rule_id, type, severity, state,
		    dedupe_key, details, propagate, started_at, escalation_chain_id)
		 VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9::jsonb,$10,$11,NULLIF($12,'')::uuid)
		 ON CONFLICT (tenant_id, dedupe_key)
		 WHERE state IN ('ACTIVE', 'ACK')
		 DO UPDATE SET
		   severity = CASE
		              WHEN `+rankExpr("EXCLUDED.severity")+` > `+rankExpr("alarm.severity")+`
		              THEN EXCLUDED.severity ELSE alarm.severity END,
		   details = alarm.details || EXCLUDED.details
		 RETURNING id::text,
		           (xmax = 0) AS inserted,
		           severity,
		           state,
		           `+rankExpr("severity")+` > `+rankExpr("$6")+` AS escalated`,
		id, in.TenantID, in.DeviceID, in.RuleID, in.AlarmType,
		in.Severity, StateActive, in.DedupeKey, mustJSON(details),
		in.Propagate, now, in.EscalationChainID)

	var outcome Outcome
	err := row.Scan(&outcome.AlarmID, &outcome.Created,
		&outcome.Severity, &outcome.State, &outcome.Escalated)
	if err != nil {
		return Outcome{}, fmt.Errorf("create alarm: %w", err)
	}

	s.publish(Event{
		AlarmID:   outcome.AlarmID,
		TenantID:  in.TenantID,
		DeviceID:  in.DeviceID,
		Type:      in.AlarmType,
		Severity:  outcome.Severity,
		State:     outcome.State,
		DedupeKey: in.DedupeKey,
		Created:   outcome.Created,
		Escalated: outcome.Escalated,
		Details:   details,
		StartedAt: now.Format(time.RFC3339),
	})
	return outcome, nil
}

// rankExpr renders the severity-rank CASE for a column or parameter.
func rankExpr(target string) string {
	return fmt.Sprintf(
		`CASE %s WHEN 'CRITICAL' THEN 4 WHEN 'MAJOR' THEN 3
		 WHEN 'MINOR' THEN 2 WHEN 'WARNING' THEN 1 ELSE 0 END`, target)
}

// ClearAlarm clears the live alarm keyed by dedupeKey.
func (s *Service) ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (Outcome, error) {
	now := time.Now().UTC()
	row := s.pool.QueryRow(ctx,
		`UPDATE alarm
		 SET state = 'CLEARED', cleared_at = $3, ended_at = $3
		 WHERE tenant_id = $1 AND dedupe_key = $2
		   AND state IN ('ACTIVE', 'ACK')
		 RETURNING id::text, type, severity, state, started_at, dedupe_key`,
		tenantID, dedupeKey, now)

	var outcome Outcome
	var alarmType, returnedDedupe string
	var startedAt time.Time
	err := row.Scan(&outcome.AlarmID, &alarmType, &outcome.Severity,
		&outcome.State, &startedAt, &returnedDedupe)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("clear alarm: %w", err)
	}

	s.publish(Event{
		AlarmID:   outcome.AlarmID,
		TenantID:  tenantID,
		DeviceID:  deviceID,
		Type:      alarmType,
		Severity:  outcome.Severity,
		State:     StateCleared,
		DedupeKey: dedupeKey,
		StartedAt: startedAt.UTC().Format(time.RFC3339),
		ClearedAt: now.Format(time.RFC3339),
	})
	return outcome, nil
}

// ErrNotFound signals that the referenced alarm does not exist in the
// tenant. The REST layer maps it to 404.
var ErrNotFound = errors.New("alarm not found")

// scopedAlarmPredicate narrows an alarm-scoped statement to alarms whose
// device the caller may see under their data scope. The alarm table carries
// device_id but no ownership columns, so visibility is membership in the
// visible device set; an unrestricted (tenant-wide) scope yields no clause.
// existing is the number of bind parameters the host query already uses.
func (s *Service) scopedAlarmPredicate(ctx context.Context, existing int) (string, []any) {
	return rbac.ScopeFromContext(ctx).DeviceSubqueryPredicate("device_id", existing)
}

// AckAlarm acknowledges the live alarm. Acknowledging an already ACK
// alarm is an idempotent no-op; a missing alarm yields ErrNotFound.
func (s *Service) AckAlarm(ctx context.Context, tenantID, alarmID string) error {
	ackClause, ackArgs := s.scopedAlarmPredicate(ctx, 2)
	tag, err := s.pool.Exec(ctx,
		`UPDATE alarm SET state = 'ACK', acked_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND state = 'ACTIVE'`+ackClause,
		append([]any{tenantID, alarmID}, ackArgs...)...)
	if err != nil {
		return fmt.Errorf("ack alarm: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	// Distinguish an out-of-scope/missing alarm (404) from an already-ACK one
	// (idempotent success). The scope clause makes them indistinguishable on
	// purpose, matching the device read surface.
	var exists bool
	exClause, exArgs := s.scopedAlarmPredicate(ctx, 2)
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM alarm
		               WHERE tenant_id = $1 AND id = $2`+exClause+`)`,
		append([]any{tenantID, alarmID}, exArgs...)...).Scan(&exists); err != nil {
		return fmt.Errorf("check alarm: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// ClearActiveForDevice clears every live alarm of one device. It supports
// the ownership transfer policy in ADR-0013: operators may choose to
// close unresolved alarms instead of carrying them with the device. It
// returns the number of alarms cleared.
func (s *Service) ClearActiveForDevice(ctx context.Context, tenantID, deviceID string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE alarm
		 SET state = 'CLEARED',
		     cleared_at = now(),
		     ended_at = now()
		 WHERE tenant_id = $1 AND device_id = $2
		   AND state IN ('ACTIVE', 'ACK')`,
		tenantID, deviceID)
	if err != nil {
		return 0, fmt.Errorf("clear device alarms: %w", err)
	}
	return tag.RowsAffected(), nil
}

// publish emits one alarm event to the per-device Core NATS subject.
func (s *Service) publish(event Event) {
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("%s.%s.%s",
		SubjectPrefix, event.TenantID, event.DeviceID)
	_ = s.nc.Publish(subject, raw)
}

// mustJSON encodes trusted detail maps.
func mustJSON(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
