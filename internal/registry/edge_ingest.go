// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// edge_ingest.go implements the ADR-0032 auto-ingest path that turns a profile
// reported by a gateway into a tenant-level device_profile. It is deliberately
// separate from the operator-facing CreateProfile/UpdateProfile:
//
//   - It only writes the EDGE-OWNED columns (data_type, unit, read_write,
//     address, edge_default, edge_min, edge_max) and never the operator
//     calibration columns (transform_type/scale/offset/enum_map/breakpoints/
//     expression/precision/valid_min/valid_max).
//   - An accepted report fills only fields the report actually populates:
//     a blank protocol family, blank scalar, or an empty {} address leaves
//     the stored value untouched. Minimal discovery frames (EdgeX device
//     services ship neither protocol family nor pre-provisioned device
//     addresses) therefore never clobber values an operator filled by hand.
//   - It never silently overwrites. A same-name profile whose content hash
//     changed is reported as Drift and left untouched until an explicit
//     AcceptEdgeProfile; a same-name operator-owned profile (source != edge)
//     is reported as a Collision.
//
// All decisions are taken inside one transaction with the profile row locked,
// so concurrent replays from several gateways cannot interleave create/drift
// outcomes.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EdgePoint is one point in the vendor-neutral reported model, already mapped
// by the caller (the edgeprofile package) off the wire. The registry never
// parses Address: it stores the opaque object verbatim.
type EdgePoint struct {
	// Name is the point name and becomes measurement_key.
	Name string
	// DataType is the neutral enum bool|int|uint|float|string|binary.
	DataType string
	// Unit is the engineering unit reported by the edge.
	Unit string
	// ReadWrite is "R", "RW" or "" when unknown.
	ReadWrite string
	// Address is the opaque per-protocol addressing object.
	Address json.RawMessage
	// EdgeDefault is the reported default value as text.
	EdgeDefault string
	// EdgeMin/EdgeMax are the reported engineering range.
	EdgeMin *float64
	EdgeMax *float64
}

// EdgeProfileUpsert is one normalized profile offered by a gateway.
type EdgeProfileUpsert struct {
	// Name is the tenant-unique profile name.
	Name string
	// Protocol is the inferred/open protocol family ("" unknown).
	Protocol string
	// ContentHash is the canonical hash of the reported payload.
	ContentHash string
	// Payload is the verbatim reported JSON kept for re-export/debug.
	Payload json.RawMessage
	// Points is the ordered, de-duplicated neutral point set.
	Points []EdgePoint
	// Force applies a detected drift instead of reporting it. It backs the
	// operator "accept new version" action.
	Force bool
}

// EdgeProfileUpsertResult classifies what one ingest did, without mutating on
// drift/collision. Exactly one of Created/Unchanged/Drift/Collision is true
// for a non-error return.
type EdgeProfileUpsertResult struct {
	// ProfileID is the resolved library profile id.
	ProfileID string
	// Created reports a new source=edge profile was inserted.
	Created bool
	// Unchanged reports the same hash was already present (only provenance
	// last_seen refreshed).
	Unchanged bool
	// Drift reports a same-name edge profile whose hash differs; the stored
	// content was left untouched.
	Drift bool
	// Collision reports a same-name operator-owned profile (source != edge);
	// the edge report was ignored.
	Collision bool
	// PointCount is the number of descriptors synced (created or resync).
	PointCount int
}

// UpsertEdgeProfile auto-ingests one reported profile for a gateway. It writes
// the per-gateway provenance row, creates the library profile on first sight,
// refreshes on identical hash, and otherwise reports drift/collision. Tenant
// and gateway identity are supplied by the authenticated transport, never the
// frame.
func (a *Admin) UpsertEdgeProfile(ctx context.Context, tenantID, gatewayID string, in EdgeProfileUpsert) (EdgeProfileUpsertResult, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		profileID, storedSource, storedHash string
		row                                 pgx.Row
	)
	row = tx.QueryRow(ctx,
		`SELECT id::text, source, content_hash
		 FROM device_profile
		 WHERE tenant_id = $1 AND name = $2
		 FOR UPDATE`,
		tenantID, in.Name)
	err = row.Scan(&profileID, &storedSource, &storedHash)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		profileID = uuid.NewString()
		if _, err := tx.Exec(ctx,
			`INSERT INTO device_profile
			   (id, tenant_id, name, source, protocol, edge_payload,
			    content_hash, availability)
			 VALUES ($1,$2,$3,'edge',$4,$5,$6,'discovered')`,
			profileID, tenantID, in.Name, in.Protocol,
			normalizeJSON(in.Payload, "{}"), in.ContentHash); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		if err := syncEdgeDescriptors(ctx, tx, tenantID, profileID, in.Points); err != nil {
			return EdgeProfileUpsertResult{}, err
		}
		if err := upsertGatewaySource(ctx, tx, profileID, gatewayID, in.ContentHash); err != nil {
			return EdgeProfileUpsertResult{}, err
		}
		if err := refreshAvailability(ctx, tx, profileID); err != nil {
			return EdgeProfileUpsertResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		return EdgeProfileUpsertResult{
			ProfileID: profileID, Created: true, PointCount: len(in.Points),
		}, nil

	case err != nil:
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}

	// Profile already exists: always record this gateway as a source.
	if err := upsertGatewaySource(ctx, tx, profileID, gatewayID, in.ContentHash); err != nil {
		return EdgeProfileUpsertResult{}, err
	}

	if storedSource != SourceEdge {
		if err := tx.Commit(ctx); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		return EdgeProfileUpsertResult{ProfileID: profileID, Collision: true}, nil
	}
	if storedHash == in.ContentHash && !in.Force {
		if err := refreshAvailability(ctx, tx, profileID); err != nil {
			return EdgeProfileUpsertResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		return EdgeProfileUpsertResult{ProfileID: profileID, Unchanged: true}, nil
	}
	if storedHash == in.ContentHash && in.Force {
		// Nothing new to accept; treat as unchanged.
		if err := tx.Commit(ctx); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		return EdgeProfileUpsertResult{ProfileID: profileID, Unchanged: true}, nil
	}

	// Hash differs: drift. Only an explicit Force accepts the new version.
	if !in.Force {
		if err := tx.Commit(ctx); err != nil {
			return EdgeProfileUpsertResult{}, mapSQLError(err)
		}
		return EdgeProfileUpsertResult{ProfileID: profileID, Drift: true}, nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE device_profile
		 SET protocol = COALESCE(NULLIF($3,''), protocol),
		     edge_payload = $4, content_hash = $5,
		     updated_at = now()
		 WHERE id = $1 AND tenant_id = $2`,
		profileID, tenantID, in.Protocol,
		normalizeJSON(in.Payload, "{}"), in.ContentHash); err != nil {
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}
	if err := syncEdgeDescriptors(ctx, tx, tenantID, profileID, in.Points); err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	if err := refreshAvailability(ctx, tx, profileID); err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}
	// Edge-accepted descriptor changes (read_write, inactive pruning) may
	// alter automatic command catalog entries of bound devices.
	a.materializeCommands(ctx, func(ctx context.Context) error {
		return a.commandMaterializer.MaterializeProfile(ctx, tenantID, profileID)
	})
	return EdgeProfileUpsertResult{
		ProfileID: profileID, Unchanged: true, PointCount: len(in.Points),
	}, nil
}

// syncEdgeDescriptors reconciles the edge-owned descriptor columns, keyed by
// measurement_key. Existing operator calibration is preserved: only edge
// columns are overwritten. Points present in storage but absent from the
// report are marked inactive rather than deleted, protecting history and rule
// references.
func syncEdgeDescriptors(ctx context.Context, tx pgx.Tx, tenantID, profileID string, points []EdgePoint) error {
	existing := map[string]string{}
	rows, err := tx.Query(ctx,
		`SELECT measurement_key, id::text FROM measurement_descriptor
		 WHERE profile_id = $1`, profileID)
	if err != nil {
		return fmt.Errorf("load edge descriptors: %w", err)
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			rows.Close()
			return err
		}
		existing[key] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, point := range points {
		if point.Name == "" || seen[point.Name] {
			continue
		}
		seen[point.Name] = true
		address := normalizeJSON(point.Address, "{}")
		if id, ok := existing[point.Name]; ok {
			// Non-empty-only merge (ADR-0032 §5, revised): a minimal discovery
			// frame reports blanks and an empty {} address. Fill only fields
			// that actually carry a value, preserving anything an operator
			// maintained by hand. Address treats {} as "no value"; numeric
			// range bounds are kept when the frame sends NULL.
			if _, err := tx.Exec(ctx,
				`UPDATE measurement_descriptor
				 SET data_type = COALESCE(NULLIF($3,''), data_type),
				     unit = COALESCE(NULLIF($4,''), unit),
				     read_write = COALESCE(NULLIF($5,''), read_write),
				     address = CASE WHEN $6 = '{}'::jsonb THEN address ELSE $6 END,
				     edge_default = COALESCE(NULLIF($7,''), edge_default),
				     edge_min = COALESCE($8, edge_min),
				     edge_max = COALESCE($9, edge_max),
				     inactive = FALSE, updated_at = now()
				 WHERE id = $1 AND tenant_id = $2`,
				id, tenantID, point.DataType, point.Unit, point.ReadWrite,
				address, point.EdgeDefault, point.EdgeMin, point.EdgeMax); err != nil {
				return mapSQLError(err)
			}
			continue
		}
		id := uuid.NewString()
		if _, err := tx.Exec(ctx,
			`INSERT INTO measurement_descriptor
			   (id, tenant_id, profile_id, measurement_key, unit,
			    transform_type, scale, offset_value, breakpoints, enum_map,
			    expression, precision, version,
			    data_type, read_write, address, edge_default, edge_min, edge_max,
			    inactive)
			 VALUES ($1,$2,$3,$4,$5,'identity',1,0,'[]','{}','',-1,1,
			         $6,$7,$8,$9,$10,$11,FALSE)`,
			id, tenantID, profileID, point.Name, point.Unit,
			point.DataType, point.ReadWrite, address,
			point.EdgeDefault, point.EdgeMin, point.EdgeMax); err != nil {
			return mapSQLError(err)
		}
	}

	// Reported set no longer contains a previously known point: retire it.
	for key, id := range existing {
		if seen[key] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE measurement_descriptor SET inactive = TRUE, updated_at = now()
			 WHERE id = $1 AND tenant_id = $2`, id, tenantID); err != nil {
			return mapSQLError(err)
		}
	}
	return nil
}

// upsertGatewaySource records/refreshes the (profile, gateway) provenance row.
func upsertGatewaySource(ctx context.Context, tx pgx.Tx, profileID, gatewayID, hash string) error {
	if gatewayID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO profile_gateway_source (profile_id, gateway_id, content_hash)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (profile_id, gateway_id) DO UPDATE
		 SET content_hash = EXCLUDED.content_hash, last_seen_at = now()`,
		profileID, gatewayID, hash); err != nil {
		return mapSQLError(err)
	}
	return nil
}

// refreshAvailability recomputes discovered/active from actual references. A
// profile is active once a device or connector instance points at it; a
// connector template on the profile itself also counts as active.
func refreshAvailability(ctx context.Context, tx pgx.Tx, profileID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE device_profile p SET availability = CASE WHEN EXISTS (
		        SELECT 1 FROM device d WHERE d.profile_id = p.id
		      ) OR EXISTS (
		        SELECT 1 FROM connector_instance c WHERE c.profile_id = p.id
		      ) OR p.connector_type <> ''
		 THEN 'active' ELSE 'discovered' END
		 WHERE p.id = $1`, profileID); err != nil {
		return mapSQLError(err)
	}
	return nil
}

// normalizeJSON returns valid non-null JSON bytes, falling back to the supplied
// document for nil/empty input.
func normalizeJSON(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 {
		return []byte(fallback)
	}
	return raw
}

// ImportEdgeProfile creates a library profile from a pasted neutral/EdgeX
// document (source=import). It returns ErrConflict on an existing same-name
// profile; callers surface that so the operator picks a fresh name. The
// supplied points populate edge-owned descriptor columns.
func (a *Admin) ImportEdgeProfile(ctx context.Context, tenantID string, in EdgeProfileUpsert) (EdgeProfileUpsertResult, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existing string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM device_profile
		 WHERE tenant_id=$1 AND name=$2`, tenantID, in.Name).Scan(&existing)
	switch {
	case err == nil:
		return EdgeProfileUpsertResult{}, fmt.Errorf("%w: profile %q already exists", ErrConflict, in.Name)
	case !errors.Is(err, pgx.ErrNoRows):
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}

	profileID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_profile
		   (id, tenant_id, name, source, protocol, edge_payload,
		    content_hash, availability)
		 VALUES ($1,$2,$3,'import',$4,$5,$6,'discovered')`,
		profileID, tenantID, in.Name, in.Protocol,
		normalizeJSON(in.Payload, "{}"), in.ContentHash); err != nil {
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}
	if err := syncEdgeDescriptors(ctx, tx, tenantID, profileID, in.Points); err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	if err := refreshAvailability(ctx, tx, profileID); err != nil {
		return EdgeProfileUpsertResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EdgeProfileUpsertResult{}, mapSQLError(err)
	}
	return EdgeProfileUpsertResult{ProfileID: profileID, Created: true, PointCount: len(in.Points)}, nil
}
