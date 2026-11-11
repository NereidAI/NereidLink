// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// enforcer.go applies automatic expiry: it computes each tenant/domain
// cutoff, honors legal hold, and deletes rows past the cutoff. Raw
// telemetry deletion is additionally gated on hourly continuous-aggregate
// coverage so detail is never deleted while the rollup still has a gap.

package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Enforcer runs the periodic retention sweep.
type Enforcer struct {
	pool  *pgxpool.Pool
	store *Store
}

// NewEnforcer constructs the enforcer.
func NewEnforcer(pool *pgxpool.Pool, store *Store) *Enforcer {
	return &Enforcer{pool: pool, store: store}
}

// DomainResult reports one tenant/domain outcome of a sweep.
type DomainResult struct {
	// TenantID is the swept tenant.
	TenantID string

	// Domain is the swept domain.
	Domain string

	// Deleted is the number of rows removed.
	Deleted int64

	// Skipped reports why a domain was left untouched: "legal_hold",
	// "keep_forever", "no_cutoff" or "uncovered_rollup".
	Skipped string
}

// EnforceTenant sweeps every domain for one tenant and returns the
// per-domain results. Telemetry deletion is clamped to the oldest
// hourly-aggregate bucket so the rollup always covers the removed range.
func (e *Enforcer) EnforceTenant(ctx context.Context, tenantID string, now time.Time) ([]DomainResult, error) {
	policies, err := e.store.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	results := make([]DomainResult, 0, len(policies))
	for _, policy := range policies {
		result := DomainResult{TenantID: tenantID, Domain: policy.Domain}

		switch {
		case policy.LegalHold:
			result.Skipped = "legal_hold"
			results = append(results, result)
			continue
		case policy.RetentionDays == 0:
			result.Skipped = "keep_forever"
			results = append(results, result)
			continue
		}

		cutoff := now.AddDate(0, 0, -policy.RetentionDays)

		var deleted int64
		switch policy.Domain {
		case DomainTelemetry:
			deleted, err = e.deleteTelemetry(ctx, tenantID, cutoff)
			if err != nil {
				return nil, fmt.Errorf("enforce telemetry: %w", err)
			}
		case DomainAlarm:
			deleted, err = e.deleteAlarms(ctx, tenantID, cutoff)
		case DomainAudit:
			deleted, err = e.deleteAudit(ctx, tenantID, cutoff)
		case DomainEvent:
			deleted, err = e.deleteDeviceEvents(ctx, tenantID, cutoff)
		default:
			result.Skipped = "no_cutoff"
			results = append(results, result)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("enforce %s: %w", policy.Domain, err)
		}
		result.Deleted = deleted
		results = append(results, result)
	}
	return results, nil
}

// hourlyWatermark returns the oldest bucket boundary materialized in the
// hourly aggregate for this tenant. Detail rows older than this are fully
// covered by the rollup and safe to delete. Null (no materialized data)
// returns the zero time, which blocks all deletion: with no rollup there
// is nothing that can replace the detail.
func (e *Enforcer) hourlyWatermark(ctx context.Context, tenantID string) (time.Time, error) {
	var watermark *time.Time
	err := e.pool.QueryRow(ctx,
		`SELECT min(bucket) FROM ts_telemetry_hourly
		 WHERE tenant_id = $1`, tenantID).Scan(&watermark)
	if err != nil {
		return time.Time{}, fmt.Errorf("hourly watermark: %w", err)
	}
	if watermark == nil {
		return time.Time{}, nil
	}
	return *watermark, nil
}

// deleteTelemetry removes detail rows older than both the policy cutoff
// and the hourly rollup watermark. The watermark clamp is the continuous
// -aggregate coverage guard from the ADR.
func (e *Enforcer) deleteTelemetry(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	watermark, err := e.hourlyWatermark(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if watermark.IsZero() {
		// Nothing has been materialized; deleting detail would leave a
		// hole that no rollup can fill.
		return 0, nil
	}
	// Rows must be older than both bounds: the effective cutoff is the
	// later of policy cutoff and rollup watermark.
	effective := cutoff
	if watermark.After(cutoff) {
		effective = watermark
	}
	tag, err := e.pool.Exec(ctx,
		`DELETE FROM ts_telemetry
		 WHERE tenant_id = $1 AND time < $2`,
		tenantID, effective)
	if err != nil {
		return 0, fmt.Errorf("delete telemetry: %w", err)
	}
	return tag.RowsAffected(), nil
}

// deleteAlarms removes alarms whose lifecycle started before cutoff.
func (e *Enforcer) deleteAlarms(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	tag, err := e.pool.Exec(ctx,
		`DELETE FROM alarm
		 WHERE tenant_id = $1 AND started_at < $2`,
		tenantID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete alarms: %w", err)
	}
	return tag.RowsAffected(), nil
}

// deleteDeviceEvents removes general device domain events older than cutoff.
// device_event is append-only by application convention; retention is the one
// authorized path that physically removes old rows (migration 0054).
func (e *Enforcer) deleteDeviceEvents(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	tag, err := e.pool.Exec(ctx,
		`DELETE FROM device_event
		 WHERE tenant_id = $1 AND time < $2`,
		tenantID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete device events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// deleteAudit removes audit events older than cutoff. Audit is append
// -only by application convention; retention is the one authorized path
// that physically removes old rows.
func (e *Enforcer) deleteAudit(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	tag, err := e.pool.Exec(ctx,
		`DELETE FROM audit_event
		 WHERE tenant_id = $1 AND time < $2`,
		tenantID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete audit: %w", err)
	}
	return tag.RowsAffected(), nil
}

// EnforceAll sweeps every tenant, resolving policies to code defaults
// when a tenant has no override rows. Legal-hold rows still block the
// matching domain. It returns the total number of rows removed.
func (e *Enforcer) EnforceAll(ctx context.Context, now time.Time) (int64, error) {
	rows, err := e.pool.Query(ctx, `SELECT id FROM tenant ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var total int64
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return total, err
		}
		results, err := e.EnforceTenant(ctx, tenantID, now)
		if err != nil {
			return total, err
		}
		for _, result := range results {
			total += result.Deleted
		}
	}
	return total, rows.Err()
}

// Run executes the sweep on every tick until ctx is canceled.
func (e *Enforcer) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			deleted, err := e.EnforceAll(ctx, now.UTC())
			if err != nil {
				slog.Error("retention sweep failed", "error", err)
				continue
			}
			if deleted > 0 {
				slog.Info("retention sweep removed rows", "deleted", deleted)
			}
		}
	}
}
