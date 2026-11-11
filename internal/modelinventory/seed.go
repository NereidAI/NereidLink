// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// seed.go defines the task-family system profiles shipped with the platform
// (ADR-0039 §3.4). They are visible to every tenant, referenceable by virtual
// inference sub-devices, and protected from operator edit/delete in the
// service layer. This Go definition is the provisioning-side single source
// for new tenants; the migration-0086 SQL seed converges existing tenants to
// the exact same rows using deterministic UUIDs and the same point set. Only
// detection/v1 ships in Phase A; other task families append their own seed
// and schema without changing this one.

package modelinventory

import (
	"context"
	"fmt"
)

// SystemProfileDetectionV1 is the only task-family system profile shipped in
// Phase A. Its points are ordinary scalars/strings, so telemetry ingestion,
// rules and alarms need no inference-aware changes.
const SystemProfileDetectionV1 = "detection/v1"

// systemPoint is one seeded measurement descriptor. data_type uses the
// neutral enum from migration 0061 (float|string); transform_type is the
// identity transform.
type systemPoint struct {
	key      string
	dataType string
	unit     string
}

// detectionPoints are the detection/v1 normative points (ADR-0039 §3.4).
// Per-class <label>_count points are optional and derived from a model's
// labels, so they are intentionally not seeded here.
var detectionPoints = []systemPoint{
	{"detection_count", "float", ""},
	{"top1_label", "string", ""},
	{"top1_score", "float", ""},
	{"infer_latency_ms", "float", "ms"},
	{"detections_json", "string", ""},
}

// EnsureSystemProfile installs the detection/v1 system profile and its point
// descriptors for one tenant idempotently. It is called at tenant provisioning
// so new tenants get the same seed the migration gives existing tenants.
func (s *PGStore) EnsureSystemProfile(ctx context.Context, tenantID string) error {
	profileID := deterministicUUID(tenantID, ":device-profile:"+SystemProfileDetectionV1)

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO device_profile
		   (id, tenant_id, name, source, protocol, availability, is_system)
		 VALUES ($1,$2,$3,'manual','','active',TRUE)
		 ON CONFLICT (tenant_id, name) DO NOTHING`,
		profileID, tenantID, SystemProfileDetectionV1); err != nil {
		return fmt.Errorf("seed %s profile: %w", SystemProfileDetectionV1, err)
	}

	// Resolve the authoritative id: if a non-system manual profile with the
	// reserved name already exists, do not attach descriptors to it.
	var resolvedID string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM device_profile
		 WHERE tenant_id=$1 AND name=$2 AND is_system=TRUE`,
		tenantID, SystemProfileDetectionV1).Scan(&resolvedID)
	if err != nil {
		return fmt.Errorf("resolve %s profile: %w", SystemProfileDetectionV1, err)
	}

	for _, p := range detectionPoints {
		descID := deterministicUUID(resolvedID, ":descriptor:"+p.key)
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO measurement_descriptor
			   (id, tenant_id, profile_id, measurement_key, data_type,
			    read_write, transform_type, unit)
			 VALUES ($1,$2,$3,$4,$5,'R','identity',$6)
			 ON CONFLICT (profile_id, measurement_key) DO NOTHING`,
			descID, tenantID, resolvedID, p.key, p.dataType, p.unit); err != nil {
			return fmt.Errorf("seed %s point %s: %w",
				SystemProfileDetectionV1, p.key, err)
		}
	}
	return nil
}

// SystemProfileID returns the deterministic detection/v1 profile id for a
// tenant, so the wizard can reference it when creating a virtual sub-device.
func SystemProfileID(tenantID string) string {
	return deterministicUUID(tenantID, ":device-profile:"+SystemProfileDetectionV1)
}
