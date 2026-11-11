// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// business.go aggregates tenant business statistics over a fixed time
// range: device totals/distribution, telemetry volume series, and alarm
// and command counts grouped by lifecycle state.

package stats

import (
	"context"
	"fmt"
	"time"
)

// RangeWindow is one selectable business time range.
type RangeWindow struct {
	// window is how far back the report reaches.
	window time.Duration

	// bucket is the telemetry series bucket width.
	bucket time.Duration
}

// rangePresets maps the query parameter to its window and bucket width.
var rangePresets = map[string]RangeWindow{
	"1h":  {window: time.Hour, bucket: time.Minute},
	"24h": {window: 24 * time.Hour, bucket: time.Hour},
	"7d":  {window: 7 * 24 * time.Hour, bucket: 24 * time.Hour},
}

// BusinessReport is the tenant business statistics response.
type BusinessReport struct {
	// Range is the requested preset key.
	Range string `json:"range"`

	// Since is the window start as RFC3339 UTC.
	Since string `json:"since"`

	// GeneratedAt is the collection time as RFC3339 UTC.
	GeneratedAt string `json:"generated_at"`

	// Devices summarizes the tenant devices.
	Devices DeviceBusiness `json:"devices"`

	// Telemetry summarizes telemetry volume.
	Telemetry TelemetryBusiness `json:"telemetry"`

	// Alarms summarizes alarm counts.
	Alarms AlarmBusiness `json:"alarms"`

	// Commands summarizes command counts.
	Commands CommandBusiness `json:"commands"`

	// ActiveRuleChains is the count of currently active rule chains.
	ActiveRuleChains int `json:"active_rule_chains"`
}

// MetricCount is a label/count pair used for grouped distributions.
type MetricCount struct {
	// Label is the group value.
	Label string `json:"label"`

	// Count is the group size.
	Count int64 `json:"count"`
}

// MetricBucket is one time-bucketed series point.
type MetricBucket struct {
	// Time is the bucket start as RFC3339 UTC.
	Time string `json:"time"`

	// Count is the bucket value.
	Count int64 `json:"count"`
}

// DeviceBusiness summarizes the tenant fleet.
type DeviceBusiness struct {
	// Total is the registered device count.
	Total int64 `json:"total"`

	// Online is the currently online count.
	Online int `json:"online"`

	// Offline is Total minus Online.
	Offline int64 `json:"offline"`

	// ByProfile groups devices by profile name.
	ByProfile []MetricCount `json:"by_profile"`
}

// TelemetryBusiness summarizes telemetry volume.
type TelemetryBusiness struct {
	// Total is the point count received within the window.
	Total int64 `json:"total"`

	// Series is the bucketed volume timeline.
	Series []MetricBucket `json:"series"`
}

// AlarmBusiness summarizes alarm counts.
type AlarmBusiness struct {
	// Created is the alarm count opened within the window.
	Created int64 `json:"created"`

	// Live is the current live alarm count.
	Live int64 `json:"live"`

	// ByState groups currently stored alarms by state.
	ByState []MetricCount `json:"by_state"`

	// BySeverity groups alarms opened in the window by severity.
	BySeverity []MetricCount `json:"by_severity"`
}

// CommandBusiness summarizes command counts.
type CommandBusiness struct {
	// Created is the command count created within the window.
	Created int64 `json:"created"`

	// ByState groups commands created in the window by current state.
	ByState []MetricCount `json:"by_state"`
}

// Business aggregates the tenant report for the named range preset.
func (s *Service) Business(ctx context.Context, tenantID, rangeKey string) (BusinessReport, error) {
	preset, ok := rangePresets[rangeKey]
	if !ok {
		rangeKey = "24h"
		preset = rangePresets["24h"]
	}
	since := time.Now().UTC().Add(-preset.window)
	report := BusinessReport{
		Range:       rangeKey,
		Since:       since.Format(time.RFC3339),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.collectDevices(ctx, tenantID, &report); err != nil {
		return BusinessReport{}, err
	}
	if err := s.collectTelemetry(ctx, tenantID, since, preset, &report); err != nil {
		return BusinessReport{}, err
	}
	if err := s.collectAlarms(ctx, tenantID, since, &report); err != nil {
		return BusinessReport{}, err
	}
	if err := s.collectCommands(ctx, tenantID, since, &report); err != nil {
		return BusinessReport{}, err
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM rule_chain
		 WHERE tenant_id = $1 AND state = 'ACTIVE'`,
		tenantID).Scan(&report.ActiveRuleChains); err != nil {
		return BusinessReport{}, fmt.Errorf("count rule chains: %w", err)
	}
	normalizeReport(&report)
	return report, nil
}

// normalizeReport replaces nil slices with empty slices so the JSON
// response consistently renders [] rather than null.
func normalizeReport(report *BusinessReport) {
	if report.Devices.ByProfile == nil {
		report.Devices.ByProfile = []MetricCount{}
	}
	if report.Telemetry.Series == nil {
		report.Telemetry.Series = []MetricBucket{}
	}
	if report.Alarms.ByState == nil {
		report.Alarms.ByState = []MetricCount{}
	}
	if report.Alarms.BySeverity == nil {
		report.Alarms.BySeverity = []MetricCount{}
	}
	if report.Commands.ByState == nil {
		report.Commands.ByState = []MetricCount{}
	}
}

// collectDevices fills fleet totals and profile distribution. Decommissioned
// rows are tombstones (devices are never physically deleted), so the live
// fleet counts exclude them to keep total = online + offline meaningful.
func (s *Service) collectDevices(ctx context.Context, tenantID string, report *BusinessReport) error {
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM device
		 WHERE tenant_id = $1 AND state <> 'DECOMMISSIONED'`,
		tenantID).Scan(&report.Devices.Total); err != nil {
		return fmt.Errorf("count devices: %w", err)
	}

	if s.online != nil {
		online, err := s.online.CountOnline(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("count online devices: %w", err)
		}
		report.Devices.Online = online
	}
	report.Devices.Offline = report.Devices.Total - int64(report.Devices.Online)

	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(p.name, 'unassigned') AS profile, count(d.id)
		 FROM device d
		 LEFT JOIN device_profile p ON p.id = d.profile_id
		 WHERE d.tenant_id = $1 AND d.state <> 'DECOMMISSIONED'
		 GROUP BY p.name ORDER BY count(d.id) DESC`,
		tenantID)
	if err != nil {
		return fmt.Errorf("group devices by profile: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var metric MetricCount
		if err := rows.Scan(&metric.Label, &metric.Count); err != nil {
			return fmt.Errorf("scan device profile group: %w", err)
		}
		report.Devices.ByProfile = append(report.Devices.ByProfile, metric)
	}
	return rows.Err()
}

// collectTelemetry fills the window total and bucketed volume series.
func (s *Service) collectTelemetry(ctx context.Context, tenantID string, since time.Time, preset RangeWindow, report *BusinessReport) error {
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ts_telemetry
		 WHERE tenant_id = $1 AND time >= $2`,
		tenantID, since).Scan(&report.Telemetry.Total); err != nil {
		return fmt.Errorf("count telemetry: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT time_bucket($2::interval, time) AS bucket, count(*)
		 FROM ts_telemetry
		 WHERE tenant_id = $1 AND time >= $3
		 GROUP BY bucket ORDER BY bucket`,
		tenantID, preset.bucket.String(), since)
	if err != nil {
		return fmt.Errorf("bucket telemetry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucket time.Time
		var count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			return fmt.Errorf("scan telemetry bucket: %w", err)
		}
		report.Telemetry.Series = append(report.Telemetry.Series, MetricBucket{
			Time:  bucket.UTC().Format(time.RFC3339),
			Count: count,
		})
	}
	return rows.Err()
}

// collectAlarms fills windowed and current alarm counts.
func (s *Service) collectAlarms(ctx context.Context, tenantID string, since time.Time, report *BusinessReport) error {
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alarm
		 WHERE tenant_id = $1 AND started_at >= $2`,
		tenantID, since).Scan(&report.Alarms.Created); err != nil {
		return fmt.Errorf("count created alarms: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alarm
		 WHERE tenant_id = $1 AND state IN ('ACTIVE', 'ACK')`,
		tenantID).Scan(&report.Alarms.Live); err != nil {
		return fmt.Errorf("count live alarms: %w", err)
	}

	var err error
	if report.Alarms.ByState, err = s.groupCounts(ctx,
		`SELECT state, count(*) FROM alarm
		 WHERE tenant_id = $1 GROUP BY state`, tenantID); err != nil {
		return err
	}
	if report.Alarms.BySeverity, err = s.groupCounts(ctx,
		`SELECT severity, count(*) FROM alarm
		 WHERE tenant_id = $1 AND started_at >= $2
		 GROUP BY severity`, tenantID, since); err != nil {
		return err
	}
	return nil
}

// collectCommands fills windowed command counts by current state.
func (s *Service) collectCommands(ctx context.Context, tenantID string, since time.Time, report *BusinessReport) error {
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM command
		 WHERE tenant_id = $1 AND created_at >= $2`,
		tenantID, since).Scan(&report.Commands.Created); err != nil {
		return fmt.Errorf("count created commands: %w", err)
	}

	var err error
	report.Commands.ByState, err = s.groupCounts(ctx,
		`SELECT state, count(*) FROM command
		 WHERE tenant_id = $1 AND created_at >= $2
		 GROUP BY state`, tenantID, since)
	if err != nil {
		return err
	}
	return nil
}

// groupCounts runs a query whose remaining parameters follow the tenant
// id and returns grouped label/count rows in result order.
func (s *Service) groupCounts(ctx context.Context, query string, args ...any) ([]MetricCount, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("run group query: %w", err)
	}
	defer rows.Close()

	out := []MetricCount{}
	for rows.Next() {
		var metric MetricCount
		if err := rows.Scan(&metric.Label, &metric.Count); err != nil {
			return nil, fmt.Errorf("scan group row: %w", err)
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}
