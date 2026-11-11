// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy_store.go persists per-tenant and per-gateway auto-provisioning
// overrides in discovery_policy (migration 0045). Nullable columns encode
// tri-state "inherit next level"; a row exists only when an operator set at
// least one field (the retention 0033 override-only pattern).

package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Policy persistence errors.
var (
	// ErrInvalidCap means a non-positive cap was supplied.
	ErrInvalidCap = errors.New("discovery policy cap must be positive")
	// ErrInvalidApprovalMode means approval_mode is neither auto nor review.
	ErrInvalidApprovalMode = errors.New("discovery approval_mode must be auto or review")
	// ErrGatewayUnknown means the gateway override target is not an
	// is_gateway device owned by the tenant.
	ErrGatewayUnknown = errors.New("gateway not found in tenant")
)

// PolicyStore is the PostgreSQL-backed policy store.
type PolicyStore struct {
	pool *pgxpool.Pool
}

// NewPolicyStore constructs a policy store.
func NewPolicyStore(pool *pgxpool.Pool) *PolicyStore {
	return &PolicyStore{pool: pool}
}

const policyColumns = `tenant_id::text, gateway_id::text,
	enabled, max_per_cycle, max_candidates, default_profile, approval_mode`

// scanRecord reads one row. NULL tri-state columns scan into nil pointers.
func scanRecord(row pgx.Row) (Record, error) {
	var tenantID string
	var gatewayID *string
	var enabled *bool
	var maxPerCycle, maxCandidates *int64
	var defaultProfile *string
	var approvalMode *string
	if err := row.Scan(&tenantID, &gatewayID, &enabled, &maxPerCycle, &maxCandidates,
		&defaultProfile, &approvalMode); err != nil {
		return Record{}, err
	}
	rec := Record{TenantID: tenantID}
	if gatewayID != nil {
		rec.GatewayID = *gatewayID
	}
	rec.Enabled = enabled
	rec.DefaultProfile = defaultProfile
	rec.ApprovalMode = approvalMode
	if maxPerCycle != nil {
		v := int(*maxPerCycle)
		rec.MaxPerCycle = &v
	}
	if maxCandidates != nil {
		v := int(*maxCandidates)
		rec.MaxCandidates = &v
	}
	return rec, nil
}

// RowsFor implements PolicyReader, returning the tenant-default and the
// gateway-override rows (nil when absent).
func (s *PolicyStore) RowsFor(ctx context.Context, tenantID, gatewayID string) (*Record, *Record, error) {
	tenantRec, err := s.fetchRow(ctx, tenantID, nil)
	if err != nil {
		return nil, nil, err
	}
	var gatewayRec *Record
	if gatewayID != "" {
		gw := gatewayID
		rec, gerr := s.fetchRow(ctx, tenantID, &gw)
		if gerr != nil {
			return nil, nil, gerr
		}
		gatewayRec = rec
	}
	return tenantRec, gatewayRec, nil
}

// fetchRow returns one override record or nil when the scope has no row.
func (s *PolicyStore) fetchRow(ctx context.Context, tenantID string, gatewayID *string) (*Record, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+policyColumns+` FROM discovery_policy
		 WHERE tenant_id=$1 AND gateway_id IS NOT DISTINCT FROM $2`,
		tenantID, gatewayID)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch discovery policy: %w", err)
	}
	return &rec, nil
}

// validateGateway ensures a gateway override target is an is_gateway device
// owned by the tenant.
func (s *PolicyStore) validateGateway(ctx context.Context, tenantID, gatewayID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device
		               WHERE id=$1 AND tenant_id=$2 AND is_gateway)`,
		gatewayID, tenantID).Scan(&ok); err != nil {
		return fmt.Errorf("validate discovery gateway: %w", err)
	}
	if !ok {
		return ErrGatewayUnknown
	}
	return nil
}

// upsert stores an override row (tenant default when gatewayID is empty).
func (s *PolicyStore) upsert(ctx context.Context, tenantID, gatewayID string, in Override) (Record, error) {
	if in.MaxPerCycle != nil && *in.MaxPerCycle <= 0 {
		return Record{}, ErrInvalidCap
	}
	if in.MaxCandidates != nil && *in.MaxCandidates <= 0 {
		return Record{}, ErrInvalidCap
	}
	if in.ApprovalMode != nil &&
		*in.ApprovalMode != ApprovalAuto && *in.ApprovalMode != ApprovalReview {
		return Record{}, ErrInvalidApprovalMode
	}
	var gwArg any
	if gatewayID != "" {
		if err := s.validateGateway(ctx, tenantID, gatewayID); err != nil {
			return Record{}, err
		}
		gwArg = gatewayID
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO discovery_policy
		    (tenant_id, gateway_id, enabled, max_per_cycle,
		     max_candidates, default_profile, approval_mode)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (tenant_id, gateway_id) DO UPDATE SET
		     enabled = EXCLUDED.enabled,
		     max_per_cycle = EXCLUDED.max_per_cycle,
		     max_candidates = EXCLUDED.max_candidates,
		     default_profile = EXCLUDED.default_profile,
		     approval_mode = EXCLUDED.approval_mode,
		     updated_at = now()`,
		tenantID, gwArg, in.Enabled, pgInt(in.MaxPerCycle), pgInt(in.MaxCandidates),
		in.DefaultProfile, in.ApprovalMode); err != nil {
		return Record{}, mapWriteError(err)
	}
	rec, err := s.fetchRow(ctx, tenantID, nullableText(gatewayID))
	if err != nil || rec == nil {
		if err == nil {
			err = pgx.ErrNoRows
		}
		return Record{}, fmt.Errorf("reload discovery policy: %w", err)
	}
	return *rec, nil
}

// pgInt widens an *int to the *int64 the driver stores, mapping nil through.
func pgInt(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

func nullableText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23514": // CHECK violation
			return ErrInvalidCap
		case "23503": // FK violation
			return ErrGatewayUnknown
		}
	}
	return fmt.Errorf("upsert discovery policy: %w", err)
}

// delete removes one override row, reverting the scope to inheritance. It is
// idempotent.
func (s *PolicyStore) delete(ctx context.Context, tenantID, gatewayID string) error {
	var gwArg any
	if gatewayID != "" {
		gwArg = gatewayID
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM discovery_policy
		 WHERE tenant_id=$1 AND gateway_id IS NOT DISTINCT FROM $2`,
		tenantID, gwArg); err != nil {
		return fmt.Errorf("delete discovery policy: %w", err)
	}
	return nil
}

// SetTenant upserts the tenant-default override.
func (s *PolicyStore) SetTenant(ctx context.Context, tenantID string, in Override) (Record, error) {
	return s.upsert(ctx, tenantID, "", in)
}

// SetGateway upserts one gateway override.
func (s *PolicyStore) SetGateway(ctx context.Context, tenantID, gatewayID string, in Override) (Record, error) {
	if gatewayID == "" {
		return Record{}, ErrGatewayUnknown
	}
	return s.upsert(ctx, tenantID, gatewayID, in)
}

// DeleteTenant removes the tenant-default override.
func (s *PolicyStore) DeleteTenant(ctx context.Context, tenantID string) error {
	return s.delete(ctx, tenantID, "")
}

// DeleteGateway removes one gateway override.
func (s *PolicyStore) DeleteGateway(ctx context.Context, tenantID, gatewayID string) error {
	return s.delete(ctx, tenantID, gatewayID)
}
