// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package alarm

import "context"

// OpenRef is a live (ACTIVE/ACK) alarm reference used by background sweeps
// that reconcile desired alarm state instead of issuing unconditional
// clears on every cycle.
type OpenRef struct {
	// TenantID owns the alarm.
	TenantID string
	// DeviceID is the alarm originator.
	DeviceID string
	// DedupeKey identifies the open alarm.
	DedupeKey string
}

// ListOpenByType returns every live alarm of one type across all tenants.
// It is a maintenance path (for example the ADR-0014 online-but-silent
// sweep), not a tenant API; callers run platform-side and must keep results
// tenant-separated when acting on them.
func (s *Service) ListOpenByType(ctx context.Context, alarmType string) ([]OpenRef, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, device_id::text, dedupe_key
		   FROM alarm
		  WHERE type = $1 AND state IN ('ACTIVE', 'ACK')`,
		alarmType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := []OpenRef{}
	for rows.Next() {
		var ref OpenRef
		if err := rows.Scan(&ref.TenantID, &ref.DeviceID, &ref.DedupeKey); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
