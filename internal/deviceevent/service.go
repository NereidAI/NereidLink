// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package deviceevent implements the general device domain-event channel:
// device-published lifecycle/domain events are persisted to the append-only
// device_event table and fanned out over Core NATS to the WebSocket plane.
//
// The platform carries no business logic of its own here: event_type,
// severity and payload are tenant-defined opaque data. This package only
// stores, routes and enforces retention; it never branches on the event
// content (that is the rule engine's job when event triggers are added).
package deviceevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// SubjectPrefix is the ingress->WebSocket fan-out subject root. Frames are
// published on "<prefix>.<tenant>.<device>"; the hub wildcard-subscribes and
// narrows by the subscriber's data scope. Token layout:
// nl(1).deviceevent(2).<tenant>(3).<device>(4).
const SubjectPrefix = "nl.deviceevent"

// Wildcard matches every device-event subject.
const Wildcard = "nl.deviceevent.>"

// Severity values accepted on ingest. Empty input defaults to info.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// ErrInvalidArgument is returned for a malformed ingest frame.
var ErrInvalidArgument = errors.New("invalid device event")

// Event is one stored domain event.
type Event struct {
	// ID is the identity row id.
	ID int64 `json:"id"`

	// TenantID owns the emitting device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the emitting device.
	DeviceID string `json:"device_id"`

	// Time is when the event occurred (RFC3339 UTC on the wire).
	Time time.Time `json:"time"`

	// EventType is the tenant-named signal label.
	EventType string `json:"event_type"`

	// Severity is info/warning/error/critical.
	Severity string `json:"severity"`

	// Payload is the raw JSON document.
	Payload json.RawMessage `json:"payload"`
}

// Frame is the transport-independent ingest input (one event).
type Frame struct {
	// EventType is required.
	EventType string `json:"event_type"`

	// Severity defaults to info when empty; validated otherwise.
	Severity string `json:"severity,omitempty"`

	// TSMS is an optional device-supplied event time in epoch milliseconds;
	// zero means the server stamps the time.
	TSMS int64 `json:"ts_ms,omitempty"`

	// Payload is the (possibly nested) JSON document, re-encoded to bytes.
	Payload any `json:"payload,omitempty"`
}

// Query narrows an event listing.
type Query struct {
	// TenantID is required.
	TenantID string

	// DeviceID, when set, restricts to one device.
	DeviceID string

	// EventType, when set, restricts to one event type.
	EventType string

	// From/To bound the time window (inclusive).
	From time.Time
	To   time.Time

	// Limit caps rows; <=0 applies the default.
	Limit int
}

// defaultListLimit bounds an unbounded query.
const defaultListLimit = 200

// maxListLimit is the hard cap for one page.
const maxListLimit = 1000

// Service persists and publishes device events.
type Service struct {
	pool *pgxpool.Pool
	nc   *nats.Conn
}

// New constructs the device-event service. nc is optional: nil makes the
// service persistence-only (no WebSocket fan-out), which keeps storage unit
// tests and bus-less environments working.
func New(pool *pgxpool.Pool, nc *nats.Conn) *Service {
	return &Service{pool: pool, nc: nc}
}

// Ingester is the narrow interface the MQTT/HTTP planes depend on.
type Ingester interface {
	Ingest(ctx context.Context, tenantID, deviceID string, frames []Frame) error
}

// Ingest validates, stores and publishes a batch of events from one device.
// A bad frame fails the batch and writes nothing.
func (s *Service) Ingest(ctx context.Context, tenantID, deviceID string, frames []Frame) error {
	if tenantID == "" || deviceID == "" {
		return fmt.Errorf("%w: tenant and device are required", ErrInvalidArgument)
	}
	if len(frames) == 0 {
		return nil
	}

	normalized := make([]Event, 0, len(frames))
	now := time.Now().UTC()
	for index, frame := range frames {
		if frame.EventType == "" {
			return fmt.Errorf("%w: frame %d missing event_type", ErrInvalidArgument, index)
		}
		severity := frame.Severity
		if severity == "" {
			severity = SeverityInfo
		}
		if !validSeverity(severity) {
			return fmt.Errorf("%w: frame %d invalid severity %q", ErrInvalidArgument, index, severity)
		}
		payload, err := encodePayload(frame.Payload)
		if err != nil {
			return fmt.Errorf("%w: frame %d payload: %v", ErrInvalidArgument, index, err)
		}
		at := now
		if frame.TSMS > 0 {
			at = time.UnixMilli(frame.TSMS).UTC()
		}
		normalized = append(normalized, Event{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			Time:      at,
			EventType: frame.EventType,
			Severity:  severity,
			Payload:   payload,
		})
	}

	for _, event := range normalized {
		var id int64
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO device_event
			   (tenant_id, device_id, time, event_type, severity, payload)
			 VALUES ($1,$2,$3,$4,$5,$6::jsonb)
			 RETURNING id`,
			event.TenantID, event.DeviceID, event.Time,
			event.EventType, event.Severity, string(event.Payload)).Scan(&id); err != nil {
			return fmt.Errorf("insert device event: %w", err)
		}
		event.ID = id
		s.publish(event)
	}
	return nil
}

// publish fans one stored event out to the WebSocket plane. Persistence has
// already succeeded, so a bus error is logged by NATS internally and never
// fails or rolls back the write (best-effort delivery, like shadow desired).
func (s *Service) publish(event Event) {
	if s.nc == nil {
		return
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("%s.%s.%s", SubjectPrefix, event.TenantID, event.DeviceID)
	_ = s.nc.Publish(subject, raw)
}

// List returns events visible under scope, newest first. A scoped member only
// sees events for devices the data scope permits; an unrestricted scope sees
// the tenant. Bind layout is fixed: tenant=1, device=2, type=3, from=4, to=5,
// limit=6; the scope EXISTS subquery then owns placeholders 7..9.
func (s *Service) List(ctx context.Context, q Query, scope rbac.DataScope) ([]Event, error) {
	if q.TenantID == "" {
		return nil, fmt.Errorf("%w: tenant is required", ErrInvalidArgument)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	// existing=6 so the rendered membership subquery uses $7 (customer),
	// $8 (groups), $9 (tenant), clear of the six fixed filters.
	predicate, scopeArgs := scope.DeviceSubqueryPredicate("e.device_id", 6)
	sql := `SELECT e.id, e.tenant_id::text, e.device_id::text, e.time,
	               e.event_type, e.severity, e.payload::text
	        FROM device_event e
	        WHERE e.tenant_id = $1
	          AND ($2 = '' OR e.device_id::text = $2)
	          AND ($3 = '' OR e.event_type = $3)
	          AND ($4::timestamptz IS NULL OR e.time >= $4)
	          AND ($5::timestamptz IS NULL OR e.time <= $5)` +
		predicate + `
	        ORDER BY e.time DESC, e.id DESC
	        LIMIT $6`

	args := []any{
		q.TenantID,
		optionalText(q.DeviceID),
		optionalText(q.EventType),
		optionalTime(q.From),
		optionalTime(q.To),
		limit,
	}
	args = append(args, scopeArgs...)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list device events: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var event Event
		var payload string
		if err := rows.Scan(&event.ID, &event.TenantID, &event.DeviceID,
			&event.Time, &event.EventType, &event.Severity, &payload); err != nil {
			return nil, fmt.Errorf("scan device event: %w", err)
		}
		event.Payload = json.RawMessage(payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

// DeleteOlder removes events before cutoff for one tenant (retention).
func (s *Service) DeleteOlder(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM device_event WHERE tenant_id = $1 AND time < $2`,
		tenantID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete device events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// encodePayload canonicalizes an arbitrary JSON document to bytes; nil
// becomes the empty object so the NOT NULL DEFAULT column always holds JSON.
func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("payload is not valid JSON")
	}
	return raw, nil
}

func validSeverity(severity string) bool {
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityError, SeverityCritical:
		return true
	default:
		return false
	}
}

func optionalText(value string) any {
	// Bind the empty string rather than NULL so "($n = '' OR ...)" matches
	// every row when the filter is unset; NULL = '' evaluates to NULL.
	return value
}

func optionalTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
