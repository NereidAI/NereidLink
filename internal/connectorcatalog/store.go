// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists edge-registered connector config schemas in
// connector_schema (migration 0043) and serves the current schema for a
// (tenant, type, version). All access is multi-tenant.

package connectorcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means no schema is registered for that type/version.
var ErrNotFound = errors.New("connector schema not found")

// Store is the PostgreSQL-backed schema catalog.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a catalog store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const schemaColumns = `id::text, tenant_id::text, connector_type,
       connector_type_version, config_schema, source,
       COALESCE(registered_by::text,''), created_at, updated_at,
       protocol, transports`

// Upsert stores (or replaces) the schema for one (tenant, type, version),
// returning the resulting row. Re-registering on every edge hello is therefore
// idempotent and keeps the catalog at the latest advertised document.
func (s *Store) Upsert(ctx context.Context, reg Registration, tenantID, registeredBy, source string) (Schema, error) {
	if source == "" {
		source = SourceEdge
	}
	var by any
	if registeredBy != "" {
		by = registeredBy
	}
	transports, err := encodeStringList(reg.Transports)
	if err != nil {
		return Schema{}, err
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO connector_schema
		    (tenant_id, connector_type, connector_type_version,
		     config_schema, source, registered_by, protocol, transports)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (tenant_id, connector_type, connector_type_version)
		 DO UPDATE SET config_schema = EXCLUDED.config_schema,
		               source = EXCLUDED.source,
		               registered_by = EXCLUDED.registered_by,
		               protocol = EXCLUDED.protocol,
		               transports = EXCLUDED.transports,
		               updated_at = now()
		 RETURNING `+schemaColumns,
		tenantID, reg.ConnectorType, reg.ConnectorTypeVersion,
		string(reg.ConfigSchema), source, by, reg.Protocol, transports)
	return scanSchema(row)
}

// encodeStringList marshals a string slice as a non-null JSON array.
func encodeStringList(items []string) ([]byte, error) {
	if items == nil {
		items = []string{}
	}
	return json.Marshal(items)
}

// Get returns the current schema for one type/version. It first tries the
// exact version, then the unversioned ("") registration when the caller asked
// for a version with no dedicated schema; ErrNotFound is returned when neither
// exists.
func (s *Store) Get(ctx context.Context, tenantID, connectorType, version string) (Schema, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+schemaColumns+`
		 FROM connector_schema
		 WHERE tenant_id=$1 AND connector_type=$2
		   AND connector_type_version=$3`,
		tenantID, connectorType, version)
	sch, err := scanSchema(row)
	if err == nil {
		return sch, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Schema{}, err
	}
	if version != "" {
		fallback := s.pool.QueryRow(ctx,
			`SELECT `+schemaColumns+`
			 FROM connector_schema
			 WHERE tenant_id=$1 AND connector_type=$2
			   AND connector_type_version=''`,
			tenantID, connectorType)
		if sch2, err2 := scanSchema(fallback); err2 == nil {
			return sch2, nil
		}
	}
	return Schema{}, ErrNotFound
}

// List returns all registered schemas for one tenant, newest updates first.
func (s *Store) List(ctx context.Context, tenantID string) ([]Schema, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+schemaColumns+`
		 FROM connector_schema WHERE tenant_id=$1
		 ORDER BY connector_type, connector_type_version`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list connector schemas: %w", err)
	}
	defer rows.Close()
	out := []Schema{}
	for rows.Next() {
		sch, err := scanSchema(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sch)
	}
	return out, rows.Err()
}

// HighestVersion returns the highest registered non-empty connector_type_version
// for one tenant's connector type, or "" with ErrNotFound when the type has no
// versioned (or any) registration. Version ordering is semantic
// (numeric-dot aware, see CompareVersions), not lexicographic, so that "1.10"
// ranks above "1.9". It backs NL's unified plugin-version selection
// (ADR-0026: the platform, not the edge, chooses the plugin version).
func (s *Store) HighestVersion(ctx context.Context, tenantID, connectorType string) (string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT connector_type_version FROM connector_schema
		 WHERE tenant_id=$1 AND connector_type=$2 AND connector_type_version<>''`,
		tenantID, connectorType)
	if err != nil {
		return "", fmt.Errorf("list connector versions: %w", err)
	}
	defer rows.Close()
	best := ""
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		if best == "" || CompareVersions(v, best) > 0 {
			best = v
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if best == "" {
		return "", ErrNotFound
	}
	return best, nil
}

// Family returns the declared protocol family for a connector type, scanning
// its registered rows for any non-empty family. It returns "" with no error
// when the type is unregistered or declares no family (fail-open).
func (s *Store) Family(ctx context.Context, tenantID, connectorType string) (string, error) {
	var family string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(protocol),'') FROM connector_schema
		 WHERE tenant_id=$1 AND connector_type=$2`,
		tenantID, connectorType).Scan(&family)
	if err != nil {
		return "", fmt.Errorf("lookup connector family: %w", err)
	}
	return family, nil
}

// rowScanner accepts pgx.Row or pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanSchema(row rowScanner) (Schema, error) {
	var sch Schema
	var raw, transports []byte
	err := row.Scan(&sch.ID, &sch.TenantID, &sch.ConnectorType,
		&sch.ConnectorTypeVersion, &raw, &sch.Source, &sch.RegisteredBy,
		&sch.CreatedAt, &sch.UpdatedAt, &sch.Protocol, &transports)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schema{}, ErrNotFound
	}
	if err != nil {
		return Schema{}, fmt.Errorf("scan connector schema: %w", err)
	}
	sch.ConfigSchema = raw
	if len(transports) > 0 {
		if err := json.Unmarshal(transports, &sch.Transports); err != nil {
			return Schema{}, fmt.Errorf("decode transports: %w", err)
		}
	}
	if sch.Transports == nil {
		sch.Transports = []string{}
	}
	return sch, nil
}
