// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// history.go persists one row per reconciled manifest cycle. The insert is
// best-effort from Reconcile: presence convergence is the authoritative
// effect, so a history failure is logged rather than failing uplink handling.

package manifest

import (
	"context"
)

// recordHistory inserts the cycle outcome into gateway_manifest.
func (r *Reconciler) recordHistory(ctx context.Context, report Report) error {
	if r.pool == nil {
		return nil
	}
	// Go nil slices encode as SQL NULL, but the history columns are NOT
	// NULL and the API contract emits arrays as [], never null. Coalesce
	// every set to a non-nil slice before the insert.
	nonNil := func(ids []string) []string {
		if ids == nil {
			return []string{}
		}
		return ids
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO gateway_manifest
		    (tenant_id, gateway_id, observed_at, revision, missed_cycles,
		     in_sync, missing, unregistered, online_promoted,
		     offline_derived, drift)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		report.TenantID, report.GatewayID, report.At, report.Revision,
		report.MissedCycles,
		nonNil(report.Class.InSync), nonNil(report.Class.Missing),
		nonNil(report.Class.Unregistered),
		report.OnlinePromoted, report.OfflineDerived, report.Class.Drift())
	return err
}
