// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// materialize.go derives per-device command catalog entries from the
// measurement descriptors of the device's bound profile. It keeps the
// platform protocol-agnostic: descriptors already carry a vendor-neutral
// read_write semantic ('R', 'W', 'RW'), and materialization only decides
// which catalog directions exist — the edge still maps the command name to
// its own south protocol when the command is dispatched.
//
// Ownership rules:
//   - Only source='auto' rows are managed here. Manual declarations are
//     never inserted over, modified or deleted.
//   - An existing automatic row keeps operator local overrides (enabled
//     switch, timeout, direction flags): re-materialization adds new names
//     and removes names that disappeared, nothing else.
//   - Inactive descriptors and descriptors without a known read_write
//     produce no command; their stale automatic rows are removed.

package command

import (
	"context"
	"fmt"
)

// autoDefaultTimeoutMS is the response wait for profile-derived read
// commands when the operator has not tuned the entry.
const autoDefaultTimeoutMS int64 = 10_000

// ProfileMaterializer reconciles automatic catalog entries from profiles.
// *Store implements it; registry keeps an optional interface reference to
// avoid an import cycle.
type ProfileMaterializer interface {
	// MaterializeDevice recomputes automatic entries for one device from
	// its currently bound profile. An empty profileID removes every
	// automatic entry (profile detached).
	MaterializeDevice(ctx context.Context, tenantID, deviceID, profileID string) error

	// MaterializeProfile recomputes automatic entries for every ACTIVE
	// device bound to the profile (used after a profile edit).
	MaterializeProfile(ctx context.Context, tenantID, profileID string) error

	// MaterializeAll backfills automatic entries for every device with a
	// bound profile (one-off admin/upgrade reconciliation).
	MaterializeAll(ctx context.Context) error
}

var _ ProfileMaterializer = (*Store)(nil)

// autoPoint is one descriptor projected to a catalog direction mask.
type autoPoint struct {
	name       string
	allowRead  bool
	allowWrite bool
}

// MaterializeDevice implements ProfileMaterializer.
func (s *Store) MaterializeDevice(ctx context.Context, tenantID, deviceID, profileID string) error {
	if profileID == "" {
		return s.DeleteAutoCatalog(ctx, tenantID, deviceID, map[string]struct{}{})
	}
	points, err := s.profilePoints(ctx, tenantID, profileID)
	if err != nil {
		return err
	}
	return s.applyAutoPoints(ctx, tenantID, deviceID, points)
}

// MaterializeProfile implements ProfileMaterializer.
func (s *Store) MaterializeProfile(ctx context.Context, tenantID, profileID string) error {
	deviceIDs, err := s.boundActiveDevices(ctx, tenantID, profileID)
	if err != nil {
		return err
	}
	for _, deviceID := range deviceIDs {
		if err := s.MaterializeDevice(ctx, tenantID, deviceID, profileID); err != nil {
			return err
		}
	}
	return nil
}

// MaterializeAll implements ProfileMaterializer.
func (s *Store) MaterializeAll(ctx context.Context) error {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, id::text, profile_id::text
		 FROM device
		 WHERE state = 'ACTIVE' AND profile_id IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("list profiled devices: %w", err)
	}
	type binding struct{ tenantID, deviceID, profileID string }
	var bindings []binding
	for rows.Next() {
		var b binding
		if err := rows.Scan(&b.tenantID, &b.deviceID, &b.profileID); err != nil {
			rows.Close()
			return fmt.Errorf("scan profiled devices: %w", err)
		}
		bindings = append(bindings, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate profiled devices: %w", err)
	}
	for _, b := range bindings {
		if err := s.MaterializeDevice(ctx, b.tenantID, b.deviceID, b.profileID); err != nil {
			return err
		}
	}
	return nil
}

// profilePoints projects active descriptors into catalog direction masks.
func (s *Store) profilePoints(ctx context.Context, tenantID, profileID string) ([]autoPoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT measurement_key, read_write
		 FROM measurement_descriptor
		 WHERE tenant_id = $1 AND profile_id = $2 AND inactive = false`,
		tenantID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile descriptors: %w", err)
	}
	defer rows.Close()

	points := []autoPoint{}
	for rows.Next() {
		var name, rw string
		if err := rows.Scan(&name, &rw); err != nil {
			return nil, fmt.Errorf("scan descriptor: %w", err)
		}
		point := autoPoint{name: name}
		switch rw {
		case "R":
			point.allowRead = true
		case "W":
			point.allowWrite = true
		case "RW":
			point.allowRead = true
			point.allowWrite = true
		default:
			// Unknown/legacy descriptor: do not guess a direction.
			continue
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

// boundActiveDevices lists ACTIVE devices bound to one profile.
func (s *Store) boundActiveDevices(ctx context.Context, tenantID, profileID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text FROM device
		 WHERE tenant_id = $1 AND profile_id = $2 AND state = 'ACTIVE'`,
		tenantID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list bound devices: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan bound device: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// applyAutoPoints inserts new automatic entries and prunes stale ones.
func (s *Store) applyAutoPoints(ctx context.Context, tenantID, deviceID string, points []autoPoint) error {
	keep := make(map[string]struct{}, len(points))
	for _, point := range points {
		keep[point.name] = struct{}{}
		inserted, err := s.UpsertAutoCatalog(ctx, tenantID, deviceID, CatalogInput{
			Name:             point.name,
			Interaction:      InteractionRequestResponse,
			Timeliness:       TimelinessRealtime,
			DefaultTimeoutMS: autoDefaultTimeoutMS,
			AllowRead:        point.allowRead,
			AllowWrite:       point.allowWrite,
			Enabled:          true,
		})
		if err != nil {
			return err
		}
		_ = inserted
	}
	return s.DeleteAutoCatalog(ctx, tenantID, deviceID, keep)
}
