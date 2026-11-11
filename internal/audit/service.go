// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the audit facade: append, query, verify and sealing.
// All writers pass through Append so the advisory-lock serialization
// and chain hashing cannot be bypassed.

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chainLockKey identifies the transaction advisory lock serializing
// audit appends cluster-wide.
const chainLockKey int64 = 4213370015

// QueryFilter narrows an audit listing.
type QueryFilter struct {
	// From bounds the start time (inclusive); zero means unbounded.
	From time.Time

	// To bounds the end time (inclusive); zero means unbounded.
	To time.Time

	// EventType filters by catalog type; empty means any.
	EventType string

	// TenantID filters by tenant; empty means any.
	TenantID string

	// ActorID filters by actor; empty means any.
	ActorID string
}

// Service is the audit facade.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs the audit service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Append inserts one event with chain enrichment. A failure is real
// (callers decide whether their operation can tolerate it) and is
// never swallowed inside this method, per ADR-0029.
func (s *Service) Append(ctx context.Context, input Input) (Event, error) {
	detailsJSON, err := canonicalDetails(input.Details)
	if err != nil {
		return Event{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Hold the chain lock until the row is committed, so concurrent
	// appends across replicas serialize instead of forking hashes.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock($1)`, chainLockKey); err != nil {
		return Event{}, fmt.Errorf("lock audit chain: %w", err)
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM audit_event`).Scan(&seq); err != nil {
		return Event{}, fmt.Errorf("next audit seq: %w", err)
	}

	var prevHash string
	if err := tx.QueryRow(ctx,
		`SELECT hash FROM audit_event WHERE seq = $1 - 1`,
		seq).Scan(&prevHash); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, fmt.Errorf("load previous audit hash: %w", err)
	}

	// Truncate to microseconds: PostgreSQL timestamptz stores microsecond
	// precision, so hashing nanoseconds would make the row fail
	// verification once read back from the database.
	eventTime := time.Now().UTC().Truncate(time.Microsecond)
	content := canonicalContent(seq, eventTime, input, detailsJSON)
	hash := hashRow(prevHash, content)

	var tenantID, actorID any
	if input.TenantID != "" {
		tenantID = input.TenantID
	}
	if input.ActorID != "" {
		actorID = input.ActorID
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_event
		   (seq, time, tenant_id, actor_id, actor_name, event_type, action,
		    resource_type, resource_id, details, ip, prev_hash, hash)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13)`,
		seq, eventTime, tenantID, actorID, input.ActorName, input.EventType,
		input.Action, input.ResourceType, input.ResourceID, string(detailsJSON),
		input.IP, prevHash, hash); err != nil {
		return Event{}, fmt.Errorf("insert audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Event{}, err
	}

	if input.Details == nil {
		input.Details = map[string]any{}
	}
	return Event{
		Seq:          seq,
		Time:         eventTime.UTC().Format(time.RFC3339Nano),
		TenantID:     input.TenantID,
		ActorID:      input.ActorID,
		ActorName:    input.ActorName,
		EventType:    input.EventType,
		Action:       input.Action,
		ResourceType: input.ResourceType,
		ResourceID:   input.ResourceID,
		Details:      input.Details,
		IP:           input.IP,
		PrevHash:     prevHash,
		Hash:         hash,
	}, nil
}

// Query returns one page of audit rows filtered, newest first.
func (s *Service) Query(ctx context.Context, filter QueryFilter,
	limit int, offset int) ([]Event, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM audit_event
		 WHERE ($3 = '' OR event_type = $3)
		   AND ($4 = '' OR tenant_id = NULLIF($4,'')::uuid)
		   AND ($5 = '' OR actor_id = NULLIF($5,'')::uuid)
		   AND ($1::timestamptz IS NULL OR time >= $1)
		   AND ($2::timestamptz IS NULL OR time <= $2)`,
		nullableTime(filter.From), nullableTime(filter.To),
		filter.EventType, filter.TenantID, filter.ActorID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT seq, time, COALESCE(tenant_id::text,''), COALESCE(actor_id::text,''),
		        actor_name, event_type, action, resource_type, resource_id,
		        details::text, ip, prev_hash, hash
		 FROM audit_event
		 WHERE ($3 = '' OR event_type = $3)
		   AND ($4 = '' OR tenant_id = NULLIF($4,'')::uuid)
		   AND ($5 = '' OR actor_id = NULLIF($5,'')::uuid)
		   AND ($1::timestamptz IS NULL OR time >= $1)
		   AND ($2::timestamptz IS NULL OR time <= $2)
		 ORDER BY seq DESC
		 LIMIT $6 OFFSET $7`,
		nullableTime(filter.From), nullableTime(filter.To),
		filter.EventType, filter.TenantID, filter.ActorID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var event Event
		var eventTime time.Time
		var detailsJSON []byte
		if err := rows.Scan(&event.Seq, &eventTime, &event.TenantID, &event.ActorID,
			&event.ActorName, &event.EventType, &event.Action,
			&event.ResourceType, &event.ResourceID, &detailsJSON,
			&event.IP, &event.PrevHash, &event.Hash); err != nil {
			return nil, 0, err
		}
		event.Time = eventTime.UTC().Format(time.RFC3339Nano)
		if err := json.Unmarshal(detailsJSON, &event.Details); err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

// VerifyResult reports a chain verification.
type VerifyResult struct {
	// OK means every row linked correctly.
	OK bool `json:"ok"`

	// EventsChecked is the row count examined.
	EventsChecked int `json:"events_checked"`

	// SealsChecked is the daily seal count verified.
	SealsChecked int `json:"seals_checked"`

	// BrokenAtSeq identifies the first forking row; zero when intact.
	BrokenAtSeq int64 `json:"broken_at_seq,omitempty"`

	// Reason describes the failure.
	Reason string `json:"reason,omitempty"`
}

// Verify recomputes hashes from the first row and checks daily seals.
// It reads the complete table; audit volumes are far below telemetry
// volumes, and a full scan is precisely the tamper check needed.
func (s *Service) Verify(ctx context.Context) (VerifyResult, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT seq, time, COALESCE(tenant_id::text,''), COALESCE(actor_id::text,''),
		        actor_name, event_type, action, resource_type, resource_id,
		        details::text, ip, prev_hash, hash
		 FROM audit_event ORDER BY seq`)
	if err != nil {
		return VerifyResult{}, err
	}
	defer rows.Close()

	prevHash := genesisHash
	expectedSeq := int64(1)
	count := 0

	for rows.Next() {
		var event Event
		var eventTime time.Time
		var detailsJSON []byte
		if err := rows.Scan(&event.Seq, &eventTime, &event.TenantID, &event.ActorID,
			&event.ActorName, &event.EventType, &event.Action,
			&event.ResourceType, &event.ResourceID, &detailsJSON,
			&event.IP, &event.PrevHash, &event.Hash); err != nil {
			return VerifyResult{}, err
		}

		if event.Seq != expectedSeq {
			return VerifyResult{
				OK: false, EventsChecked: count, BrokenAtSeq: event.Seq,
				Reason: fmt.Sprintf("seq gap: expected %d got %d", expectedSeq, event.Seq),
			}, nil
		}
		if event.PrevHash != prevHash {
			return VerifyResult{
				OK: false, EventsChecked: count, BrokenAtSeq: event.Seq,
				Reason: "previous hash does not link the prior row",
			}, nil
		}

		input := Input{
			TenantID:     event.TenantID,
			ActorID:      event.ActorID,
			ActorName:    event.ActorName,
			EventType:    event.EventType,
			Action:       event.Action,
			ResourceType: event.ResourceType,
			ResourceID:   event.ResourceID,
			IP:           event.IP,
		}
		// Normalize details before hashing: jsonb canonical text does
		// not preserve key order, while the append path hashes Go's
		// key-sorted JSON. Re-marshal to that canonical form.
		var detailsMap map[string]any
		if err := json.Unmarshal(detailsJSON, &detailsMap); err != nil {
			return VerifyResult{}, err
		}
		canonicalJSON, err := canonicalDetails(detailsMap)
		if err != nil {
			return VerifyResult{}, err
		}
		content := canonicalContent(event.Seq, eventTime, input, canonicalJSON)
		if hashRow(prevHash, content) != event.Hash {
			return VerifyResult{
				OK: false, EventsChecked: count, BrokenAtSeq: event.Seq,
				Reason: "stored hash does not match recomputed content",
			}, nil
		}

		prevHash = event.Hash
		expectedSeq++
		count++
	}
	if err := rows.Err(); err != nil {
		return VerifyResult{}, err
	}

	seals, err := s.verifySeals(ctx)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		OK: true, EventsChecked: count, SealsChecked: seals,
	}, nil
}

// verifySeals checks every daily seal row against the chain state.
func (s *Service) verifySeals(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT day, last_seq, last_hash FROM audit_daily_seal ORDER BY day`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var day time.Time
		var lastSeq int64
		var lastHash string
		if err := rows.Scan(&day, &lastSeq, &lastHash); err != nil {
			return 0, err
		}
		var actualHash string
		err := s.pool.QueryRow(ctx,
			`SELECT hash FROM audit_event WHERE seq = $1`,
			lastSeq).Scan(&actualHash)
		if errors.Is(err, pgx.ErrNoRows) || actualHash != lastHash {
			return 0, fmt.Errorf("seal for %s does not match chain", day.Format("2006-01-02"))
		}
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, rows.Err()
}

// Run periodically seals completed days. Hourly checks keep the seal
// lag bounded without scheduling complexity.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	seal := func() {
		if err := s.sealCompletedDays(ctx); err != nil {
			slog.Error("audit daily seal failed", "error", err)
		}
	}
	verify := func() {
		result, err := s.Verify(ctx)
		switch {
		case err != nil:
			slog.Error("audit chain verification failed", "error", err)
		case !result.OK:
			slog.Error("audit hash chain is broken",
				"broken_at_seq", result.BrokenAtSeq, "reason", result.Reason)
		}
	}
	seal()
	verify()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			seal()
			verify()
		}
	}
}

// sealCompletedDays records the final event of every fully past day
// that has no seal yet. Idempotent by primary key: reruns never
// duplicate a seal.
func (s *Service) sealCompletedDays(ctx context.Context) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_daily_seal (day, last_seq, last_hash)
		 SELECT last_day::date, seq, hash
		 FROM (
		     SELECT date_trunc('day', time) AS last_day,
		            (ARRAY_AGG(seq ORDER BY seq DESC))[1] AS seq,
		            (ARRAY_AGG(hash ORDER BY seq DESC))[1] AS hash
		     FROM audit_event
		     WHERE time < date_trunc('day', now())
		     GROUP BY date_trunc('day', time)
		 ) days
		 ON CONFLICT (day) DO NOTHING`)
	return err
}

// nullableTime returns nil when a time is zero, enabling SQL NULL.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
