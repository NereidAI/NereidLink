// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStoreNotFound is the storage-level absence marker. The owner translates
// endpoint lookups to ErrEndpointNotFound; callers use errors.Is.
var ErrStoreNotFound = errors.New("companion store record not found")

// Store is the persistence seam for the owner. The PostgreSQL implementation
// below is production; tests substitute an in-memory implementation so the
// full reconcile logic runs without a database.
type Store interface {
	// GetEndpoint selects the installation for one logical companion on one
	// gateway, including disabled rows (the owner enforces the enabled gate).
	GetEndpoint(ctx context.Context, tenantID, companionID, gatewayID string) (Endpoint, error)
	// GetEndpointByID loads one endpoint by its surrogate id.
	GetEndpointByID(ctx context.Context, tenantID, endpointID string) (Endpoint, error)
	// UpsertEndpoint inserts an endpoint or updates the row matching the
	// (tenant, companion, gateway) natural key, returning the stored row.
	UpsertEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error)
	// ListEndpoints returns every endpoint of one tenant, newest first.
	ListEndpoints(ctx context.Context, tenantID string) ([]Endpoint, error)
	// DeleteEndpoint removes an endpoint; desired rows cascade away.
	DeleteEndpoint(ctx context.Context, tenantID, endpointID string) error
	// ReplaceDesired performs a full replacement of the rows owned by one
	// business artifact under an endpoint.
	ReplaceDesired(ctx context.Context, tenantID, endpointID, artifactKey string, rows []DesiredRow) error
	// DeleteDesiredArtifact removes every row owned by one artifact and
	// returns the endpoint id plus the remaining desired row count.
	DeleteDesiredArtifact(ctx context.Context, tenantID, artifactKey string) (endpointID string, remaining int, err error)
	// ListDesired returns the full desired set for an endpoint in stable
	// source-device order.
	ListDesired(ctx context.Context, tenantID, endpointID string) ([]DesiredRow, error)
	// ApplyReadback persists companion-reported revision/status per row.
	ApplyReadback(ctx context.Context, tenantID, endpointID string, readback []ReadbackRow) error
}

// PGStore is the pgx-backed Store implementation.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore constructs the PostgreSQL store.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// GetEndpoint implements Store.
func (s *PGStore) GetEndpoint(ctx context.Context, tenantID, companionID, gatewayID string) (Endpoint, error) {
	const q = `
		SELECT id, tenant_id, companion_id, gateway_id, base_url,
		       contract_version, auth_token, insecure_skip_verify,
		       enabled, created_at, updated_at
		  FROM companion_endpoint
		 WHERE tenant_id=$1 AND companion_id=$2 AND gateway_id=$3`
	return s.queryEndpoint(ctx, q, tenantID, companionID, gatewayID)
}

// GetEndpointByID implements Store.
func (s *PGStore) GetEndpointByID(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	const q = `
		SELECT id, tenant_id, companion_id, gateway_id, base_url,
		       contract_version, auth_token, insecure_skip_verify,
		       enabled, created_at, updated_at
		  FROM companion_endpoint
		 WHERE tenant_id=$1 AND id=$2`
	return s.queryEndpoint(ctx, q, tenantID, endpointID)
}

func (s *PGStore) queryEndpoint(ctx context.Context, q string, args ...any) (Endpoint, error) {
	var ep Endpoint
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&ep.ID, &ep.TenantID, &ep.CompanionID, &ep.GatewayID, &ep.BaseURL,
		&ep.ContractVersion, &ep.AuthToken, &ep.InsecureSkipVerify,
		&ep.Enabled, &ep.CreatedAt, &ep.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrStoreNotFound
	}
	if err != nil {
		return Endpoint{}, err
	}
	return ep, nil
}

// UpsertEndpoint implements Store against the natural key
// (tenant_id, companion_id, gateway_id). The surrogate id is database-owned.
func (s *PGStore) UpsertEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error) {
	const q = `
		INSERT INTO companion_endpoint (
		    tenant_id, companion_id, gateway_id, base_url, contract_version,
		    auth_token, insecure_skip_verify, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, companion_id, gateway_id) DO UPDATE SET
		    base_url=EXCLUDED.base_url,
		    contract_version=EXCLUDED.contract_version,
		    auth_token=EXCLUDED.auth_token,
		    insecure_skip_verify=EXCLUDED.insecure_skip_verify,
		    enabled=EXCLUDED.enabled,
		    updated_at=now()
		RETURNING id, tenant_id, companion_id, gateway_id, base_url,
		          contract_version, auth_token, insecure_skip_verify,
		          enabled, created_at, updated_at`
	var ep Endpoint
	err := s.pool.QueryRow(ctx, q,
		endpoint.TenantID, endpoint.CompanionID, endpoint.GatewayID,
		endpoint.BaseURL, endpoint.ContractVersion, endpoint.AuthToken,
		endpoint.InsecureSkipVerify, endpoint.Enabled).Scan(
		&ep.ID, &ep.TenantID, &ep.CompanionID, &ep.GatewayID, &ep.BaseURL,
		&ep.ContractVersion, &ep.AuthToken, &ep.InsecureSkipVerify,
		&ep.Enabled, &ep.CreatedAt, &ep.UpdatedAt)
	if err != nil {
		return Endpoint{}, err
	}
	return ep, nil
}

// ListEndpoints implements Store.
func (s *PGStore) ListEndpoints(ctx context.Context, tenantID string) ([]Endpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, companion_id, gateway_id, base_url,
		       contract_version, auth_token, insecure_skip_verify,
		       enabled, created_at, updated_at
		  FROM companion_endpoint
		 WHERE tenant_id=$1
		 ORDER BY companion_id, gateway_id, created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Endpoint, 0)
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(
			&ep.ID, &ep.TenantID, &ep.CompanionID, &ep.GatewayID, &ep.BaseURL,
			&ep.ContractVersion, &ep.AuthToken, &ep.InsecureSkipVerify,
			&ep.Enabled, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// DeleteEndpoint implements Store. Desired rows cascade via the foreign key.
func (s *PGStore) DeleteEndpoint(ctx context.Context, tenantID, endpointID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM companion_endpoint WHERE tenant_id=$1 AND id=$2`,
		tenantID, endpointID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStoreNotFound
	}
	return nil
}

// ReplaceDesired implements Store inside one transaction: the artifact's
// previous rows are deleted and the new set inserted, without touching rows
// owned by other artifacts sharing the endpoint.
func (s *PGStore) ReplaceDesired(ctx context.Context, tenantID, endpointID, artifactKey string, rows []DesiredRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM companion_desired_subscription
		  WHERE tenant_id=$1 AND endpoint_id=$2 AND artifact_key=$3`,
		tenantID, endpointID, artifactKey); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO companion_desired_subscription (
				tenant_id, endpoint_id, artifact_key, row_id, source_device,
				camera, pipeline_id, conclusion_device_id, enabled,
				revision, status, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			tenantID, endpointID, artifactKey, row.RowID, row.SourceDevice,
			row.Camera, row.PipelineID, row.ConclusionDeviceID, row.Enabled,
			row.Revision, row.Status, row.Reason); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteDesiredArtifact implements Store.
func (s *PGStore) DeleteDesiredArtifact(ctx context.Context, tenantID, artifactKey string) (string, int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var endpointID string
	err = tx.QueryRow(ctx, `
		SELECT endpoint_id FROM companion_desired_subscription
		 WHERE tenant_id=$1 AND artifact_key=$2 LIMIT 1`,
		tenantID, artifactKey).Scan(&endpointID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing desired locally: treat as already removed, idempotent.
		return "", 0, tx.Commit(ctx)
	}
	if err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM companion_desired_subscription
		  WHERE tenant_id=$1 AND artifact_key=$2`,
		tenantID, artifactKey); err != nil {
		return "", 0, err
	}
	var remaining int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM companion_desired_subscription
		 WHERE tenant_id=$1 AND endpoint_id=$2`,
		tenantID, endpointID).Scan(&remaining); err != nil {
		return "", 0, err
	}
	return endpointID, remaining, tx.Commit(ctx)
}

// ListDesired implements Store.
func (s *PGStore) ListDesired(ctx context.Context, tenantID, endpointID string) ([]DesiredRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT row_id, source_device, camera, pipeline_id,
		       conclusion_device_id, enabled, artifact_key,
		       revision, status, reason
		  FROM companion_desired_subscription
		 WHERE tenant_id=$1 AND endpoint_id=$2
		 ORDER BY source_device, row_id`,
		tenantID, endpointID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DesiredRow, 0)
	for rows.Next() {
		var row DesiredRow
		if err := rows.Scan(&row.RowID, &row.SourceDevice, &row.Camera,
			&row.PipelineID, &row.ConclusionDeviceID, &row.Enabled,
			&row.ArtifactKey, &row.Revision, &row.Status, &row.Reason); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ApplyReadback implements Store. Rows absent from the read-back keep their
// previous state rather than being silently marked failed.
func (s *PGStore) ApplyReadback(ctx context.Context, tenantID, endpointID string, readback []ReadbackRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, rb := range readback {
		if _, err := tx.Exec(ctx, `
			UPDATE companion_desired_subscription
			   SET revision=$4, status=$5, reason=$6, updated_at=now()
			 WHERE tenant_id=$1 AND endpoint_id=$2 AND row_id=$3`,
			tenantID, endpointID, rb.ID, rb.Revision, rb.Status, rb.Reason); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
