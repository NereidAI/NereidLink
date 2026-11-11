// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// candidate_store.go persists the human-approval queue for gateway
// auto-provisioning (ADR-0026, migration 0046). PENDING rows are created only
// when the effective policy approval_mode is "review"; an operator later
// approves (which creates the device) or rejects. The table is append-only: a
// decision moves the row to a terminal state, it never deletes it.

package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CandidateRecord persistence / decision errors.
var (
	// ErrCandidateNotFound means no pending candidate matches the id.
	ErrCandidateNotFound = errors.New("discovery candidate not found")
	// ErrCandidateNotPending means the candidate already has a decision.
	ErrCandidateNotPending = errors.New("discovery candidate already decided")
	// ErrPendingExists means a PENDING row already owns the gateway-local id;
	// repeated discovery is idempotent rather than a duplicate insert.
	ErrPendingExists = errors.New("pending discovery candidate already exists")
)

// CandidateRecord states (mirror the migration CHECK).
const (
	StatePending  = "PENDING"
	StateApproved = "APPROVED"
	StateRejected = "REJECTED"
	StateExpired  = "EXPIRED"
)

// CandidateRecord is one queued discovery request.
type CandidateRecord struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	GatewayID      string     `json:"gateway_id"`
	LocalID        string     `json:"local_id"`
	CandidateName  string     `json:"candidate_name,omitempty"`
	ProfileID      string     `json:"profile_id,omitempty"`
	SourceType     string     `json:"source_type,omitempty"`
	State          string     `json:"state"`
	DeviceID       string     `json:"device_id,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// NewCandidate builds a pending enqueue input.
type NewCandidate struct {
	TenantID      string
	GatewayID     string
	LocalID       string
	CandidateName string
	ProfileID     string
	SourceType    string
}

// CandidateStore persists discovery candidates.
type CandidateStore struct {
	pool *pgxpool.Pool
}

// NewCandidateStore constructs a candidate store.
func NewCandidateStore(pool *pgxpool.Pool) *CandidateStore {
	return &CandidateStore{pool: pool}
}

const candidateColumns = `id::text, tenant_id::text, gateway_id::text, local_id,
	candidate_name, COALESCE(profile_id::text,''), source_type, state,
	COALESCE(device_id::text,''), decision_reason, COALESCE(decided_by,''),
	decided_at, created_at`

func scanCandidate(row pgx.Row) (CandidateRecord, error) {
	var c CandidateRecord
	if err := row.Scan(&c.ID, &c.TenantID, &c.GatewayID, &c.LocalID,
		&c.CandidateName, &c.ProfileID, &c.SourceType, &c.State,
		&c.DeviceID, &c.DecisionReason, &c.DecidedBy,
		&c.DecidedAt, &c.CreatedAt); err != nil {
		return CandidateRecord{}, err
	}
	return c, nil
}

// PendingInsert adds one PENDING candidate. A duplicate live request for the
// same (gateway, local_id) returns ErrPendingExists (the caller reports the
// existing request as pending rather than failing the frame).
func (s *CandidateStore) PendingInsert(ctx context.Context, in NewCandidate) (CandidateRecord, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO discovery_candidate
		    (tenant_id, gateway_id, local_id, candidate_name,
		     profile_id, source_type, state)
		 VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,'PENDING')
		 ON CONFLICT DO NOTHING
		 RETURNING `+candidateColumns,
		in.TenantID, in.GatewayID, in.LocalID, in.CandidateName,
		in.ProfileID, in.SourceType)
	c, err := scanCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// The ON CONFLICT partial-index branch: a PENDING row already owns
		// this gateway-local id.
		return CandidateRecord{}, ErrPendingExists
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CandidateRecord{}, ErrPendingExists
		}
		return CandidateRecord{}, fmt.Errorf("insert discovery candidate: %w", err)
	}
	return c, nil
}

// ListCandidates returns one tenant's candidates, optionally filtered by
// state and gateway, newest first with total count.
func (s *CandidateStore) ListCandidates(ctx context.Context, tenantID, state, gatewayID string,
	limit, offset int) ([]CandidateRecord, int, error) {
	var args []any
	where := []string{"tenant_id = $1"}
	args = append(args, tenantID)
	if state != "" {
		args = append(args, strings.ToUpper(state))
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if gatewayID != "" {
		args = append(args, gatewayID)
		where = append(where, fmt.Sprintf("gateway_id = $%d", len(args)))
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM discovery_candidate WHERE `+clause, args...).
		Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count discovery candidates: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx,
		`SELECT `+candidateColumns+` FROM discovery_candidate WHERE `+clause+
			fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list discovery candidates: %w", err)
	}
	defer rows.Close()

	out := []CandidateRecord{}
	for rows.Next() {
		c, scanErr := scanCandidate(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// GetPendingCandidate loads one tenant's PENDING candidate by id.
func (s *CandidateStore) GetPendingCandidate(ctx context.Context, tenantID, candidateID string) (CandidateRecord, error) {
	c, err := s.GetByID(ctx, tenantID, candidateID)
	if err != nil {
		return CandidateRecord{}, err
	}
	if c.State != StatePending {
		return c, ErrCandidateNotPending
	}
	return c, nil
}

// GetByID loads one tenant's candidate by id regardless of state.
func (s *CandidateStore) GetByID(ctx context.Context, tenantID, candidateID string) (CandidateRecord, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+candidateColumns+` FROM discovery_candidate
		 WHERE id=$1 AND tenant_id=$2`,
		candidateID, tenantID)
	c, err := scanCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CandidateRecord{}, ErrCandidateNotFound
	}
	if err != nil {
		return CandidateRecord{}, fmt.Errorf("get discovery candidate: %w", err)
	}
	return c, nil
}

// MarkApproved records the created device on an approved candidate.
func (s *CandidateStore) MarkApproved(ctx context.Context, tenantID, candidateID, deviceID, actor, reason string) error {
	return s.decide(ctx, tenantID, candidateID, StateApproved, deviceID, actor, reason)
}

// MarkRejected rejects a candidate without creating a device.
func (s *CandidateStore) MarkRejected(ctx context.Context, tenantID, candidateID, actor, reason string) error {
	return s.decide(ctx, tenantID, candidateID, StateRejected, "", actor, reason)
}

// decide conditionally moves one PENDING row to a terminal state. The
// state='PENDING' predicate makes a concurrent second decision affect no rows.
func (s *CandidateStore) decide(ctx context.Context, tenantID, candidateID, state, deviceID, actor, reason string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE discovery_candidate
		 SET state=$1, device_id=NULLIF($2,'')::uuid, decided_by=$3,
		     decision_reason=$4, decided_at=now(), updated_at=now()
		 WHERE id=$5 AND tenant_id=$6 AND state='PENDING'`,
		state, deviceID, actor, reason, candidateID, tenantID)
	if err != nil {
		return fmt.Errorf("decide discovery candidate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish a missing row from an already-decided one.
		var current string
		lErr := s.pool.QueryRow(ctx,
			`SELECT state FROM discovery_candidate WHERE id=$1 AND tenant_id=$2`,
			candidateID, tenantID).Scan(&current)
		if errors.Is(lErr, pgx.ErrNoRows) {
			return ErrCandidateNotFound
		}
		if lErr != nil {
			return fmt.Errorf("check discovery candidate: %w", lErr)
		}
		return ErrCandidateNotPending
	}
	return nil
}
