// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means the cached edge profile does not exist.
var ErrNotFound = errors.New("edge profile not found")

// Store persists gateway_edge_profile rows.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store over the pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cachedColumns = `
	tenant_id::text, gateway_id::text, name, content_hash, resource_count,
	first_seen_at, last_seen_at,
	COALESCE(adopted_profile_id::text, ''), adopted_at`

func scanCached(row pgx.Row) (Profile, CachedProfile, error) {
	var (
		profile Profile
		meta    CachedProfile
		payload []byte
	)
	err := row.Scan(
		&meta.TenantID, &meta.GatewayID, &meta.Name, &meta.ContentHash,
		&meta.ResourceCount, &meta.FirstSeenAt, &meta.LastSeenAt,
		&meta.AdoptedProfileID, &meta.AdoptedAt, &payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, CachedProfile{}, ErrNotFound
		}
		return Profile{}, CachedProfile{}, fmt.Errorf("scan cached edge profile: %w", err)
	}
	if err := json.Unmarshal(payload, &profile); err != nil {
		return Profile{}, CachedProfile{}, fmt.Errorf("decode cached edge profile: %w", err)
	}
	return profile, meta, nil
}

// Report upserts one reported profile. last_seen_at always refreshes (so hello
// replays prove liveness); content_hash, payload and resource_count are
// overwritten with the latest report (identical bytes when unchanged). The
// adoption linkage columns are never touched here, so re-reporting a profile
// does not detach a native profile adopted from it.
func (s *Store) Report(ctx context.Context, tenantID, gatewayID string, profile Profile, hash string) error {
	raw, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("encode edge profile payload: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO gateway_edge_profile
		    (tenant_id, gateway_id, name, content_hash, payload,
		     resource_count, first_seen_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $7)
		 ON CONFLICT (tenant_id, gateway_id, name) DO UPDATE
		 SET last_seen_at = EXCLUDED.last_seen_at,
		     content_hash = EXCLUDED.content_hash,
		     payload = EXCLUDED.payload,
		     resource_count = EXCLUDED.resource_count`,
		tenantID, gatewayID, profile.Name, hash, string(raw),
		len(profile.DeviceResources), now)
	if err != nil {
		return fmt.Errorf("upsert edge profile: %w", err)
	}
	return nil
}

// ListForGateway returns cached profile metadata (no raw payload) for one
// gateway, ordered by name. Always returns a non-nil slice.
func (s *Store) ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]CachedProfile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, gateway_id::text, name, content_hash,
		        resource_count, first_seen_at, last_seen_at,
		        COALESCE(adopted_profile_id::text, ''), adopted_at
		 FROM gateway_edge_profile
		 WHERE tenant_id = $1 AND gateway_id = $2
		 ORDER BY name`, tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list edge profiles: %w", err)
	}
	defer rows.Close()

	out := []CachedProfile{}
	for rows.Next() {
		var meta CachedProfile
		if err := rows.Scan(
			&meta.TenantID, &meta.GatewayID, &meta.Name, &meta.ContentHash,
			&meta.ResourceCount, &meta.FirstSeenAt, &meta.LastSeenAt,
			&meta.AdoptedProfileID, &meta.AdoptedAt); err != nil {
			return nil, fmt.Errorf("scan edge profile summary: %w", err)
		}
		out = append(out, meta)
	}
	return out, rows.Err()
}

// Get returns one cached profile and its metadata, decoding the raw payload.
func (s *Store) Get(ctx context.Context, tenantID, gatewayID, name string) (Profile, CachedProfile, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+cachedColumns+`, payload
		 FROM gateway_edge_profile
		 WHERE tenant_id = $1 AND gateway_id = $2 AND name = $3`,
		tenantID, gatewayID, name)
	return scanCached(row)
}

// SetAdoption links a cached edge profile to the native profile created or
// updated by an adopt action and stamps adopted_at.
func (s *Store) SetAdoption(ctx context.Context, tenantID, gatewayID, name, nativeProfileID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE gateway_edge_profile
		 SET adopted_profile_id = $4, adopted_at = now()
		 WHERE tenant_id = $1 AND gateway_id = $2 AND name = $3`,
		tenantID, gatewayID, name, nativeProfileID)
	if err != nil {
		return fmt.Errorf("link adopted edge profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
