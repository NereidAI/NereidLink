// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists per-tenant retention overrides and legal-hold flags.

package retention

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the retention policy store backed by PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a retention store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Get returns the effective policy for one tenant/domain. Missing rows
// resolve to the code default with LegalHold false.
func (s *Store) Get(ctx context.Context, tenantID, domain string) (Policy, error) {
	if _, ok := Defaults[domain]; !ok {
		return Policy{}, ErrInvalidDomain
	}
	defaultDays := Defaults[domain]

	var days int
	var legalHold bool
	err := s.pool.QueryRow(ctx,
		`SELECT retention_days, legal_hold
		 FROM retention_policy
		 WHERE tenant_id = $1 AND domain = $2`,
		tenantID, domain).Scan(&days, &legalHold)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{
			TenantID:      tenantID,
			Domain:        domain,
			RetentionDays: defaultDays,
			Overridden:    false,
		}, nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("get retention policy: %w", err)
	}
	return Policy{
		TenantID:      tenantID,
		Domain:        domain,
		RetentionDays: days,
		LegalHold:     legalHold,
		Overridden:    days != defaultDays,
	}, nil
}

// List returns the effective policy for every domain of a tenant, in
// Domains() order. Missing rows resolve to defaults.
func (s *Store) List(ctx context.Context, tenantID string) ([]Policy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT domain, retention_days, legal_hold
		 FROM retention_policy
		 WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list retention policy: %w", err)
	}
	defer rows.Close()

	overrides := map[string]Policy{}
	for rows.Next() {
		var domain string
		var p Policy
		if err := rows.Scan(&domain, &p.RetentionDays, &p.LegalHold); err != nil {
			return nil, err
		}
		p.TenantID = tenantID
		p.Domain = domain
		overrides[domain] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Policy, 0, len(Domains()))
	for _, domain := range Domains() {
		if p, ok := overrides[domain]; ok {
			p.Overridden = p.RetentionDays != Defaults[domain]
			out = append(out, p)
			continue
		}
		out = append(out, Policy{
			TenantID:      tenantID,
			Domain:        domain,
			RetentionDays: Defaults[domain],
		})
	}
	return out, nil
}

// SetInput changes one tenant/domain policy.
type SetInput struct {
	// RetentionDays is the new window; 0 keeps data indefinitely.
	RetentionDays int

	// LegalHold suspends automatic deletion.
	LegalHold bool
}

// Set upserts one policy row. The effective policy is returned.
func (s *Store) Set(ctx context.Context, tenantID, domain string, input SetInput) (Policy, error) {
	if _, ok := Defaults[domain]; !ok {
		return Policy{}, ErrInvalidDomain
	}
	if input.RetentionDays < 0 {
		return Policy{}, ErrInvalidDays
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO retention_policy (tenant_id, domain, retention_days, legal_hold)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, domain)
		 DO UPDATE SET retention_days = EXCLUDED.retention_days,
		               legal_hold     = EXCLUDED.legal_hold,
		               updated_at     = now()`,
		tenantID, domain, input.RetentionDays, input.LegalHold); err != nil {
		return Policy{}, fmt.Errorf("set retention policy: %w", err)
	}
	return s.Get(ctx, tenantID, domain)
}
