// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists global and private operation descriptors and per-tenant
// disable marks (migration 0070).

package opcatalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// descriptorColumns is the shared descriptor projection.
const descriptorColumns = `operation, type_version, interaction, timeout_ms,
	param_schema::jsonb, result_schema::jsonb, risk, display_name, description`

// aliasDescriptorColumns projects the same columns through alias a.
const aliasDescriptorColumns = `a.operation, a.type_version, a.interaction,
	a.timeout_ms, a.param_schema::jsonb, a.result_schema::jsonb, a.risk,
	a.display_name, a.description`

// scanDescriptor decodes one descriptor.
func scanDescriptor(row pgx.Row) (Descriptor, error) {
	var d Descriptor
	err := row.Scan(&d.Operation, &d.TypeVersion, &d.Interaction, &d.TimeoutMs,
		&d.ParamSchema, &d.ResultSchema, &d.Risk, &d.DisplayName,
		&d.Description)
	return d, err
}

// Advertised is one gateway's advertisement row joined with the gateway name
// for tenant-scoped aggregation.
type Advertised struct {
	Descriptor
	GatewayID   string
	GatewayName string
}

// advertisedColumns projects an advertisement plus its gateway identity.
const advertisedColumns = `a.operation, a.type_version, a.interaction, a.timeout_ms,
	a.param_schema::jsonb, a.result_schema::jsonb, a.risk, a.display_name,
	a.description, a.gateway_id, d.canonical_name`

// scanAdvertised decodes one joined advertisement row.
func scanAdvertised(row pgx.Row) (Advertised, error) {
	var a Advertised
	err := row.Scan(&a.Operation, &a.TypeVersion, &a.Interaction, &a.TimeoutMs,
		&a.ParamSchema, &a.ResultSchema, &a.Risk, &a.DisplayName,
		&a.Description, &a.GatewayID, &a.GatewayName)
	return a, err
}

// Store is the PostgreSQL catalog store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ReplaceGatewayAdvertisements reconciles one gateway's advertised set in a
// single transaction: every accepted descriptor is upserted and any prior row
// for this gateway absent from the accepted full set is deleted. hello
// carries the complete current set, so disappearance means removal.
func (s *Store) ReplaceGatewayAdvertisements(
	ctx context.Context, gatewayID string, accepted []Descriptor,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace gateway operations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	keys := make([][]string, 0, len(accepted))
	for _, d := range accepted {
		if _, err := tx.Exec(ctx,
			`INSERT INTO gateway_operation_advertisement
			 (gateway_id, operation, type_version, interaction, timeout_ms,
			  param_schema, result_schema, risk, display_name, description)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			 ON CONFLICT (gateway_id, operation, type_version) DO UPDATE
			 SET interaction=EXCLUDED.interaction,
			     timeout_ms=EXCLUDED.timeout_ms,
			     param_schema=EXCLUDED.param_schema,
			     result_schema=EXCLUDED.result_schema,
			     risk=EXCLUDED.risk,
			     display_name=EXCLUDED.display_name,
			     description=EXCLUDED.description,
			     updated_at=now()`,
			gatewayID, d.Operation, d.TypeVersion, d.Interaction, d.TimeoutMs,
			jsonRaw(d.ParamSchema), jsonRaw(d.ResultSchema), d.Risk,
			jsonMap(d.DisplayName), jsonMap(d.Description)); err != nil {
			return fmt.Errorf("upsert gateway operation advertisement: %w", err)
		}
		keys = append(keys, []string{d.Operation, d.TypeVersion})
	}

	if len(keys) == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM gateway_operation_advertisement WHERE gateway_id=$1`,
			gatewayID); err != nil {
			return fmt.Errorf("clear gateway operation advertisements: %w", err)
		}
	} else {
		opArgs := pgx.NamedArgs{"gateway_id": gatewayID}
		var orParts string
		for i, kv := range keys {
			opKey := fmt.Sprintf("op%d", i)
			verKey := fmt.Sprintf("ver%d", i)
			opArgs[opKey] = kv[0]
			opArgs[verKey] = kv[1]
			if i > 0 {
				orParts += " OR "
			}
			orParts += fmt.Sprintf("(operation=@%s AND type_version=@%s)", opKey, verKey)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM gateway_operation_advertisement
			  WHERE gateway_id=@gateway_id AND NOT (`+orParts+`)`,
			opArgs); err != nil {
			return fmt.Errorf("reconcile gateway operation advertisements: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace gateway operations: %w", err)
	}
	return nil
}

// ListGatewayAdvertisements returns every descriptor one gateway advertises.
// The tenant predicate joins device so a gateway owned by another tenant is
// invisible even to a caller that knows its id.
func (s *Store) ListGatewayAdvertisements(
	ctx context.Context, tenantID, gatewayID string,
) ([]Descriptor, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+aliasDescriptorColumns+`
		   FROM gateway_operation_advertisement a
		   JOIN device d ON d.id = a.gateway_id
		  WHERE d.tenant_id=$1 AND a.gateway_id=$2
		  ORDER BY a.operation, a.type_version`, tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list gateway operations: %w", err)
	}
	defer rows.Close()
	return collectDescriptors(rows)
}

// SelectGatewayAdvertisement resolves the newest advertised version of one
// operation for a gateway. Newest is the most recently upserted row, with
// type_version as a deterministic tiebreaker. The tenant join prevents
// resolving another tenant's gateway.
func (s *Store) SelectGatewayAdvertisement(
	ctx context.Context, tenantID, gatewayID, operation string,
) (Descriptor, bool, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+aliasDescriptorColumns+`
		   FROM gateway_operation_advertisement a
		   JOIN device d ON d.id = a.gateway_id
		  WHERE d.tenant_id=$1 AND a.gateway_id=$2 AND a.operation=$3
		  ORDER BY a.updated_at DESC, a.type_version DESC
		  LIMIT 1`, tenantID, gatewayID, operation)
	d, err := scanDescriptor(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Descriptor{}, false, nil
	}
	if err != nil {
		return Descriptor{}, false, fmt.Errorf("select gateway operation: %w", err)
	}
	return d, true, nil
}

// ListTenantAdvertised returns all advertisements owned by gateways of one
// tenant, newest version first within each key, with gateway names attached.
func (s *Store) ListTenantAdvertised(
	ctx context.Context, tenantID string,
) ([]Advertised, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+advertisedColumns+`
		   FROM gateway_operation_advertisement a
		   JOIN device d ON d.id = a.gateway_id
		  WHERE d.tenant_id=$1
		  ORDER BY a.operation, a.type_version, a.updated_at DESC,
		           a.gateway_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant advertised operations: %w", err)
	}
	defer rows.Close()
	out := []Advertised{}
	for rows.Next() {
		a, err := scanAdvertised(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreatePrivate inserts one tenant-defined descriptor.
func (s *Store) CreatePrivate(ctx context.Context, tenantID string, d Descriptor) error {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_operation
		 (tenant_id, operation, type_version, interaction, timeout_ms,
		  param_schema, result_schema, risk, display_name, description)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		tenantID, d.Operation, d.TypeVersion, d.Interaction, d.TimeoutMs,
		jsonRaw(d.ParamSchema), jsonRaw(d.ResultSchema), d.Risk,
		jsonMap(d.DisplayName), jsonMap(d.Description))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return fmt.Errorf("create tenant operation: %w", err)
	}
	_ = tag
	return nil
}

// UpdatePrivate replaces one private descriptor.
func (s *Store) UpdatePrivate(ctx context.Context, tenantID, operation, version string, d Descriptor) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenant_operation
		 SET operation=$3, type_version=$4, interaction=$5, timeout_ms=$6,
		     param_schema=$7, result_schema=$8, risk=$9,
		     display_name=$10, description=$11, updated_at=now()
		 WHERE tenant_id=$1 AND operation=$2 AND type_version=$12`,
		tenantID, operation, d.Operation, d.TypeVersion, d.Interaction,
		d.TimeoutMs, jsonRaw(d.ParamSchema), jsonRaw(d.ResultSchema), d.Risk,
		jsonMap(d.DisplayName), jsonMap(d.Description), version)
	if err != nil {
		return fmt.Errorf("update tenant operation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePrivate removes one private descriptor.
func (s *Store) DeletePrivate(ctx context.Context, tenantID, operation, version string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_operation
		 WHERE tenant_id=$1 AND operation=$2 AND type_version=$3`,
		tenantID, operation, version)
	if err != nil {
		return fmt.Errorf("delete tenant operation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPrivate returns a tenant's private descriptors.
func (s *Store) ListPrivate(ctx context.Context, tenantID string) ([]Descriptor, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+descriptorColumns+` FROM tenant_operation
		 WHERE tenant_id=$1 ORDER BY operation`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant operations: %w", err)
	}
	defer rows.Close()
	return collectDescriptors(rows)
}

// SetDisabled records a disable mark.
func (s *Store) SetDisabled(ctx context.Context, tenantID, operation, version, userID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_operation_disabled
		 (tenant_id, operation, type_version, disabled_by)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tenant_id, operation, type_version) DO NOTHING`,
		tenantID, operation, version, nullableID(userID))
	if err != nil {
		return fmt.Errorf("disable operation: %w", err)
	}
	return nil
}

// ClearDisabled removes a disable mark.
func (s *Store) ClearDisabled(ctx context.Context, tenantID, operation, version string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_operation_disabled
		 WHERE tenant_id=$1 AND operation=$2 AND type_version=$3`,
		tenantID, operation, version)
	if err != nil {
		return fmt.Errorf("enable operation: %w", err)
	}
	return nil
}

// DisabledKeys returns a tenant's disabled operation keys as key|version
// entries.
func (s *Store) DisabledKeys(ctx context.Context, tenantID string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT operation, type_version FROM tenant_operation_disabled
		 WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list disabled operations: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var operation, version string
		if err := rows.Scan(&operation, &version); err != nil {
			return nil, err
		}
		out[operation+"|"+version] = true
	}
	return out, rows.Err()
}

// collectDescriptors drains a descriptor query.
func collectDescriptors(rows pgx.Rows) ([]Descriptor, error) {
	out := []Descriptor{}
	for rows.Next() {
		d, err := scanDescriptor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// jsonRaw normalizes empty raw schema to a JSON object for a jsonb column.
func jsonRaw(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// jsonMap normalizes a nil localization map to an empty JSON object so a
// NOT NULL jsonb column receives '{}' instead of NULL (pgx encodes a nil
// map as SQL NULL, bypassing the column DEFAULT).
func jsonMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// nullableID returns nil for an empty id.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
