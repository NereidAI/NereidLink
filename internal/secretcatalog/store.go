// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists edge secret type descriptors in edge_secret_type
// (migration 0067) and serves the current descriptor for a
// (tenant, type, version). All access is multi-tenant.

package secretcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL-backed descriptor catalog.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a descriptor store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const typeColumns = `id::text, tenant_id::text, secret_type, type_version,
       fields_schema, display_name, source,
       COALESCE(registered_by::text,''), created_at, updated_at`

// uniqueViolation is the PostgreSQL SQLSTATE for a unique_violation.
const uniqueViolation = "23505"

// Upsert stores (or replaces) the descriptor for one
// (tenant, type, version), returning the resulting row. Re-registering on
// every edge hello is idempotent and keeps the catalog at the latest
// advertised document.
func (s *Store) Upsert(ctx context.Context, reg Registration, tenantID, registeredBy string) (SecretType, error) {
	displayName, err := json.Marshal(reg.DisplayName)
	if err != nil {
		return SecretType{}, fmt.Errorf("encode display_name: %w", err)
	}
	var by any
	if registeredBy != "" {
		by = registeredBy
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO edge_secret_type
		    (tenant_id, secret_type, type_version, fields_schema,
		     display_name, source, registered_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (tenant_id, secret_type, type_version)
		 DO UPDATE SET fields_schema = EXCLUDED.fields_schema,
		               display_name = EXCLUDED.display_name,
		               source = EXCLUDED.source,
		               registered_by = EXCLUDED.registered_by,
		               updated_at = now()
		 RETURNING `+typeColumns,
		tenantID, reg.SecretType, reg.TypeVersion,
		string(reg.FieldsSchema), displayName, SourceEdge, by)
	return scanType(row)
}

// Create inserts a tenant-defined descriptor, mapping a duplicate key to
// ErrConflict. It never overwrites an edge descriptor.
func (s *Store) Create(ctx context.Context, reg Registration, tenantID string) (SecretType, error) {
	displayName, err := json.Marshal(reg.DisplayName)
	if err != nil {
		return SecretType{}, fmt.Errorf("encode display_name: %w", err)
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO edge_secret_type
		    (tenant_id, secret_type, type_version, fields_schema,
		     display_name, source)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING `+typeColumns,
		tenantID, reg.SecretType, reg.TypeVersion,
		string(reg.FieldsSchema), displayName, SourceManual)
	st, err := scanType(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return SecretType{}, ErrConflict
		}
		return SecretType{}, err
	}
	return st, nil
}

// Update replaces a tenant-defined descriptor and returns the new row.
// Edge descriptors cannot be updated.
func (s *Store) Update(ctx context.Context, tenantID, secretType, version string, reg Registration) (SecretType, error) {
	displayName, err := json.Marshal(reg.DisplayName)
	if err != nil {
		return SecretType{}, fmt.Errorf("encode display_name: %w", err)
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE edge_secret_type
		    SET secret_type=$4, type_version=$5, fields_schema=$6,
		        display_name=$7, updated_at=now()
		 WHERE tenant_id=$1 AND secret_type=$2 AND type_version=$3
		   AND source=$8
		 RETURNING `+typeColumns,
		tenantID, secretType, version,
		reg.SecretType, reg.TypeVersion,
		string(reg.FieldsSchema), displayName, SourceManual)
	st, err := scanType(row)
	if errors.Is(err, ErrNotFound) {
		return SecretType{}, ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return SecretType{}, ErrConflict
		}
		return SecretType{}, err
	}
	return st, nil
}

// Delete removes a tenant-defined descriptor. Edge descriptors cannot be
// deleted. ErrNotFound is returned for a missing or edge-owned row.
func (s *Store) Delete(ctx context.Context, tenantID, secretType, version string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM edge_secret_type
		 WHERE tenant_id=$1 AND secret_type=$2 AND type_version=$3
		   AND source=$4`,
		tenantID, secretType, version, SourceManual)
	if err != nil {
		return fmt.Errorf("delete edge secret type: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns the descriptor for one type/version. It first tries the exact
// version, then the unversioned ("") registration. ErrNotFound is returned
// when neither exists.
func (s *Store) Get(ctx context.Context, tenantID, secretType, version string) (SecretType, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+typeColumns+`
		 FROM edge_secret_type
		 WHERE tenant_id=$1 AND secret_type=$2 AND type_version=$3`,
		tenantID, secretType, version)
	st, err := scanType(row)
	if err == nil {
		return st, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return SecretType{}, err
	}
	if version != "" {
		fallback := s.pool.QueryRow(ctx,
			`SELECT `+typeColumns+`
			 FROM edge_secret_type
			 WHERE tenant_id=$1 AND secret_type=$2 AND type_version=''`,
			tenantID, secretType)
		if st2, err2 := scanType(fallback); err2 == nil {
			return st2, nil
		}
	}
	return SecretType{}, ErrNotFound
}

// List returns all descriptors for one tenant, sorted by type and version.
func (s *Store) List(ctx context.Context, tenantID string) ([]SecretType, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+typeColumns+`
		 FROM edge_secret_type WHERE tenant_id=$1
		 ORDER BY source, secret_type, type_version`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list edge secret types: %w", err)
	}
	defer rows.Close()
	out := []SecretType{}
	for rows.Next() {
		st, err := scanType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// rowScanner accepts pgx.Row or pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanType(row rowScanner) (SecretType, error) {
	var st SecretType
	var raw, display []byte
	err := row.Scan(&st.ID, &st.TenantID, &st.SecretType, &st.TypeVersion,
		&raw, &display, &st.Source, &st.RegisteredBy,
		&st.CreatedAt, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SecretType{}, ErrNotFound
	}
	if err != nil {
		return SecretType{}, fmt.Errorf("scan edge secret type: %w", err)
	}
	st.FieldsSchema = raw
	if len(display) > 0 {
		if err := json.Unmarshal(display, &st.DisplayName); err != nil {
			return SecretType{}, fmt.Errorf("decode display_name: %w", err)
		}
	}
	return st, nil
}
