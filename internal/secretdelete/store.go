// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists forced-delete markers (migration 0073).

package secretdelete

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL pending-delete store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Upsert records one pending deletion. A repeat force for the same secret
// keeps the original initiator/timestamp (DO NOTHING): the deletion is
// already in progress.
func (s *Store) Upsert(ctx context.Context, p PendingDelete) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_secret_pending_delete
		 (tenant_id, secret_name, initiated_by, initiated_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tenant_id, secret_name) DO NOTHING`,
		p.TenantID, p.SecretName, nullableID(p.InitiatedBy), p.InitiatedAt)
	if err != nil {
		return fmt.Errorf("insert pending delete: %w", err)
	}
	return nil
}

// List returns every pending deletion, oldest first.
func (s *Store) List(ctx context.Context) ([]PendingDelete, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, secret_name, COALESCE(initiated_by::text,''),
		        to_char(initiated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		 FROM tenant_secret_pending_delete
		 ORDER BY initiated_at`)
	if err != nil {
		return nil, fmt.Errorf("list pending deletes: %w", err)
	}
	defer rows.Close()
	out := []PendingDelete{}
	for rows.Next() {
		var p PendingDelete
		if err := rows.Scan(&p.TenantID, &p.SecretName, &p.InitiatedBy,
			&p.InitiatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Exists reports whether one pending deletion is recorded.
func (s *Store) Exists(ctx context.Context, tenantID, secretName string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tenant_secret_pending_delete
		 WHERE tenant_id=$1 AND secret_name=$2)`,
		tenantID, secretName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check pending delete: %w", err)
	}
	return exists, nil
}

// Delete removes one pending deletion after the vault original is deleted.
func (s *Store) Delete(ctx context.Context, tenantID, secretName string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_secret_pending_delete
		 WHERE tenant_id=$1 AND secret_name=$2`,
		tenantID, secretName)
	if err != nil {
		return fmt.Errorf("delete pending marker: %w", err)
	}
	return nil
}

// nullableID maps an empty id to SQL NULL.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
