// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store loads profile projections from PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a profile store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Binding returns the profile identifier attached to a device.
func (s *Store) Binding(ctx context.Context, tenantID, deviceID string) (string, bool, error) {
	var profileID *string
	err := s.pool.QueryRow(ctx,
		`SELECT profile_id::text
		 FROM device
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID).Scan(&profileID)
	if err != nil {
		return "", false, fmt.Errorf("load device profile binding: %w", err)
	}
	if profileID == nil || *profileID == "" {
		return "", false, nil
	}
	return *profileID, true, nil
}

// ProfileAndDescriptors loads one profile and its complete descriptor set.
func (s *Store) ProfileAndDescriptors(ctx context.Context, profileID string) (Profile, map[string]Descriptor, error) {
	var profile Profile
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, tenant_id::text, name
		 FROM device_profile
		 WHERE id = $1`,
		profileID).Scan(&profile.ID, &profile.TenantID, &profile.Name)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("load device profile: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id::text,
		        tenant_id::text,
		        profile_id::text,
		        measurement_key,
		        unit,
		        transform_type,
		        scale,
		        offset_value,
		        breakpoints,
		        enum_map,
		        expression,
		        precision,
		        valid_min,
		        valid_max,
		        version
		 FROM measurement_descriptor
		 WHERE profile_id = $1`,
		profileID)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("load measurement descriptors: %w", err)
	}
	defer rows.Close()

	descriptors := map[string]Descriptor{}
	for rows.Next() {
		var descriptor Descriptor
		var breakpoints []byte
		var enumMap []byte
		if err := rows.Scan(
			&descriptor.ID,
			&descriptor.TenantID,
			&descriptor.ProfileID,
			&descriptor.MeasurementKey,
			&descriptor.Unit,
			&descriptor.TransformType,
			&descriptor.Scale,
			&descriptor.Offset,
			&breakpoints,
			&enumMap,
			&descriptor.Expression,
			&descriptor.Precision,
			&descriptor.ValidMin,
			&descriptor.ValidMax,
			&descriptor.Version,
		); err != nil {
			return Profile{}, nil, fmt.Errorf("scan measurement descriptor: %w", err)
		}
		if err := json.Unmarshal(breakpoints, &descriptor.Breakpoints); err != nil {
			return Profile{}, nil, fmt.Errorf("decode breakpoints: %w", err)
		}
		if err := json.Unmarshal(enumMap, &descriptor.EnumMap); err != nil {
			return Profile{}, nil, fmt.Errorf("decode enum map: %w", err)
		}
		sort.Slice(descriptor.Breakpoints, func(i, j int) bool {
			return descriptor.Breakpoints[i].X < descriptor.Breakpoints[j].X
		})
		descriptors[descriptor.MeasurementKey] = descriptor
	}
	if err := rows.Err(); err != nil {
		return Profile{}, nil, fmt.Errorf("iterate measurement descriptors: %w", err)
	}
	return profile, descriptors, nil
}

// Overrides loads all device-level calibration overrides.
func (s *Store) Overrides(ctx context.Context, deviceID string) (map[string]Override, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT measurement_key,
		        scale,
		        offset_value,
		        valid_min,
		        valid_max,
		        precision
		 FROM device_measurement_override
		 WHERE device_id = $1`,
		deviceID)
	if err != nil {
		return nil, fmt.Errorf("load measurement overrides: %w", err)
	}
	defer rows.Close()

	overrides := map[string]Override{}
	for rows.Next() {
		var key string
		var override Override
		if err := rows.Scan(
			&key,
			&override.Scale,
			&override.Offset,
			&override.ValidMin,
			&override.ValidMax,
			&override.Precision,
		); err != nil {
			return nil, fmt.Errorf("scan measurement override: %w", err)
		}
		overrides[key] = override
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate measurement overrides: %w", err)
	}
	return overrides, nil
}
