// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists step-up challenges in PostgreSQL (migration 0068).
// State transitions are conditional UPDATEs: the WHERE clause enforces the
// expected prior state and expiry, so concurrent/competing requests cannot
// move one row twice and a replay leaves an auditable no-op failure.

package stepup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence surface used by Service.
type Store interface {
	// Insert stores a newly started challenge.
	Insert(ctx context.Context, c Challenge) error
	// Get loads one challenge scoped to its tenant and owner.
	Get(ctx context.Context, tenantID, userID, id string) (Challenge, error)
	// MarkVerified moves pending -> verified, rejecting expired rows.
	MarkVerified(ctx context.Context, tenantID, userID, id string, at time.Time) (Challenge, error)
	// Consume moves verified -> consumed, rejecting expired rows.
	Consume(ctx context.Context, tenantID, userID, id string, at time.Time) (Challenge, error)
	// DeleteExpired removes rows past their lifetime regardless of state.
	DeleteExpired(ctx context.Context, before time.Time) error
}

// pgxStore is the PostgreSQL Store implementation.
type pgxStore struct {
	pool *pgxpool.Pool
}

// NewStore wraps a database pool.
func NewStore(pool *pgxpool.Pool) Store {
	return &pgxStore{pool: pool}
}

// columns is the shared row projection.
const columns = `id, tenant_id, user_id, action, details, details_sha256,
	status, expires_at, verified_at, consumed_at, created_at, updated_at`

// scanRow decodes one challenge row.
func scanRow(row pgx.Row) (Challenge, error) {
	var c Challenge
	err := row.Scan(&c.ID, &c.TenantID, &c.UserID, &c.Action, &c.Details,
		&c.DetailsSHA256, &c.Status, &c.ExpiresAt, &c.VerifiedAt,
		&c.ConsumedAt, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// Insert implements Store.
func (s *pgxStore) Insert(ctx context.Context, c Challenge) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO step_up_challenge
		 (id, tenant_id, user_id, action, details, details_sha256,
		  status, expires_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`,
		c.ID, c.TenantID, c.UserID, c.Action, c.Details, c.DetailsSHA256,
		c.Status, c.ExpiresAt, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert step-up challenge: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *pgxStore) Get(ctx context.Context, tenantID, userID, id string) (Challenge, error) {
	c, err := scanRow(s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM step_up_challenge
		 WHERE id = $1 AND tenant_id = $2 AND user_id = $3`,
		id, tenantID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, ErrNotFound
	}
	if err != nil {
		return Challenge{}, fmt.Errorf("load step-up challenge: %w", err)
	}
	return c, nil
}

// MarkVerified implements Store.
func (s *pgxStore) MarkVerified(ctx context.Context, tenantID, userID, id string, at time.Time) (Challenge, error) {
	return s.transition(ctx, tenantID, userID, id, at, StatusPending, StatusVerified)
}

// Consume implements Store.
func (s *pgxStore) Consume(ctx context.Context, tenantID, userID, id string, at time.Time) (Challenge, error) {
	return s.transition(ctx, tenantID, userID, id, at, StatusVerified, StatusConsumed)
}

// transition runs one guarded state change. The current row is read first to
// distinguish replay/expiry/wrong-state for auditing and error codes; the
// UPDATE then re-checks status and expiry so two concurrent requests cannot
// both succeed.
func (s *pgxStore) transition(ctx context.Context, tenantID, userID, id string, at time.Time, from, to string) (Challenge, error) {
	current, err := s.Get(ctx, tenantID, userID, id)
	if err != nil {
		return Challenge{}, err
	}
	if !current.ExpiresAt.After(at) {
		return current, ErrExpired
	}
	if current.Status != from {
		return current, stateError(from, current.Status)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE step_up_challenge
		 SET status = $2, updated_at = $3,
		     verified_at = CASE WHEN $2 = 'verified' THEN $3::timestamptz ELSE verified_at END,
		     consumed_at = CASE WHEN $2 = 'consumed' THEN $3::timestamptz ELSE consumed_at END
		 WHERE id = $1 AND tenant_id = $4 AND user_id = $5
		   AND status = $6 AND expires_at > $3`,
		id, to, at, tenantID, userID, from)
	if err != nil {
		return Challenge{}, fmt.Errorf("advance step-up challenge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Lost a concurrent race for the same transition.
		return current, stateError(from, to)
	}
	return s.Get(ctx, tenantID, userID, id)
}

// stateError maps an observed status to the right sentinel for a transition
// attempted from expected.
func stateError(expected, observed string) error {
	switch observed {
	case StatusConsumed:
		return ErrReplay
	case StatusVerified:
		if expected == StatusVerified {
			return ErrReplay
		}
		return ErrNotPending
	case StatusPending:
		return ErrNotVerified
	default:
		return ErrNotVerified
	}
}

// DeleteExpired implements Store.
func (s *pgxStore) DeleteExpired(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM step_up_challenge WHERE expires_at <= $1`, before)
	if err != nil {
		return fmt.Errorf("sweep step-up challenges: %w", err)
	}
	return nil
}
