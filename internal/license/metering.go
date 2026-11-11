// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metering.go produces and queries the daily tenant usage facts from
// ADR-0024. Billing is intentionally outside the platform; this table is
// only the source of measurement facts.

package license

import (
	"context"
	"fmt"
	"time"
)

// UsageRow is one tenant-day metering fact.
type UsageRow struct {
	// Day is the UTC usage day.
	Day string `json:"day"`

	// TenantID is the measured tenant.
	TenantID string `json:"tenant_id"`

	// DeviceCount is the registered device count used for cap metering.
	DeviceCount int `json:"device_count"`

	// ActiveDeviceCount is the count with telemetry during the day.
	ActiveDeviceCount int `json:"active_device_count"`

	// Messages is the persisted telemetry message count.
	Messages int64 `json:"messages"`

	// Bytes is the logical telemetry record size.
	Bytes int64 `json:"bytes"`

	// HotBytes is logical Hot-tier storage.
	HotBytes int64 `json:"hot_bytes"`

	// WarmBytes is logical Warm-tier storage.
	WarmBytes int64 `json:"warm_bytes"`

	// ColdBytes is logical Cold-tier storage.
	ColdBytes int64 `json:"cold_bytes"`
}

// generateUsage writes current and previous UTC day facts. Re-running is
// idempotent; previous-day device counts are preserved once recorded.
func (s *Service) generateUsage(ctx context.Context, now time.Time) error {
	today := now.Truncate(24 * time.Hour)
	if err := s.generateDayUsage(ctx, today); err != nil {
		return err
	}
	return s.generateDayUsage(ctx, today.AddDate(0, 0, -1))
}

func (s *Service) generateDayUsage(ctx context.Context, day time.Time) error {
	_, err := s.pool.Exec(ctx,
		`WITH telemetry_usage AS (
		     SELECT tenant_id,
		            count(*) AS messages,
		            count(DISTINCT device_id) AS active_device_count,
		            COALESCE(sum(pg_column_size(to_jsonb(x))), 0) AS bytes
		     FROM ts_telemetry x
		     WHERE time >= $1
		       AND time < $1 + INTERVAL '1 day'
		     GROUP BY tenant_id
		 )
		 INSERT INTO tenant_usage_daily
		     (day, tenant_id, device_count, active_device_count, messages,
		      bytes, hot_bytes, warm_bytes, cold_bytes, updated_at)
		 SELECT $1, t.id,
			COALESCE(
			    (SELECT device_count
			     FROM tenant_usage_daily old
			     WHERE old.day = $1 AND old.tenant_id = t.id),
			    (SELECT count(*) FROM device WHERE tenant_id = t.id)),
			COALESCE(u.active_device_count, 0),
			COALESCE(u.messages, 0),
			COALESCE(u.bytes, 0),
			COALESCE(u.bytes, 0),
			0, 0, now()
		 FROM tenant t
		 LEFT JOIN telemetry_usage u ON u.tenant_id = t.id
		 ON CONFLICT (day, tenant_id) DO UPDATE SET
		     active_device_count = EXCLUDED.active_device_count,
		     messages = EXCLUDED.messages,
		     bytes = EXCLUDED.bytes,
		     hot_bytes = EXCLUDED.hot_bytes,
		     warm_bytes = EXCLUDED.warm_bytes,
		     cold_bytes = EXCLUDED.cold_bytes,
		     updated_at = now()`, day)
	if err != nil {
		return fmt.Errorf("generate usage for %s: %w", day.Format("2006-01-02"), err)
	}
	return nil
}

// ListUsage returns tenant usage across a date range.
func (s *Service) ListUsage(ctx context.Context, from, to time.Time) ([]UsageRow, error) {
	return s.queryUsage(ctx,
		`SELECT day::text, tenant_id::text, device_count, active_device_count,
		        messages, bytes, hot_bytes, warm_bytes, cold_bytes
		 FROM tenant_usage_daily
		 WHERE day BETWEEN $1 AND $2
		 ORDER BY day DESC, tenant_id`, from, to)
}

// TenantUsage returns one tenant's usage across a date range.
func (s *Service) TenantUsage(ctx context.Context, tenantID string, from, to time.Time) ([]UsageRow, error) {
	return s.queryUsage(ctx,
		`SELECT day::text, tenant_id::text, device_count, active_device_count,
		        messages, bytes, hot_bytes, warm_bytes, cold_bytes
		 FROM tenant_usage_daily
		 WHERE tenant_id = $1 AND day BETWEEN $2 AND $3
		 ORDER BY day DESC`, tenantID, from, to)
}

func (s *Service) queryUsage(ctx context.Context, sql string, args ...any) ([]UsageRow, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rowsOut := []UsageRow{}
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(&row.Day, &row.TenantID, &row.DeviceCount,
			&row.ActiveDeviceCount, &row.Messages, &row.Bytes, &row.HotBytes,
			&row.WarmBytes, &row.ColdBytes); err != nil {
			return nil, err
		}
		rowsOut = append(rowsOut, row)
	}
	return rowsOut, rows.Err()
}
