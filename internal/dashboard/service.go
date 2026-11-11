// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package dashboard aggregates tenant operational snapshots for the
// Overview page. It is a read-only projection across registry, alarms,
// commands, notifications, telemetry and live presence; it owns no state
// and introduces no background workers.
package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PresenceCounter counts live presence keys of one tenant.
type PresenceCounter interface {
	// CountOnline returns the number of devices with a live KV key.
	CountOnline(ctx context.Context, tenantID string) (int, error)
}

// MetricCount is one labeled count used by chart series.
type MetricCount struct {
	// Label identifies the bucket or category.
	Label string `json:"label"`

	// Count is the number of matching records.
	Count int64 `json:"count"`
}

// RecentAlarm is a compact alarm row for the Overview feed.
type RecentAlarm struct {
	// ID is the alarm identifier.
	ID string `json:"id"`

	// DeviceID is the alarm originator.
	DeviceID string `json:"device_id"`

	// Type classifies the alarm.
	Type string `json:"type"`

	// Severity is CRITICAL, MAJOR, MINOR or WARNING.
	Severity string `json:"severity"`

	// State is ACTIVE, ACK or CLEARED.
	State string `json:"state"`

	// StartedAt is the alarm creation time as RFC3339 UTC.
	StartedAt string `json:"started_at"`
}

// RecentNotification is a compact inbox row for the Overview feed.
type RecentNotification struct {
	// ID is the notification identifier.
	ID string `json:"id"`

	// DeviceID is the related device when present.
	DeviceID string `json:"device_id"`

	// Title is the notification headline.
	Title string `json:"title"`

	// Body contains additional context.
	Body string `json:"body"`

	// Severity is CRITICAL, MAJOR, MINOR, WARNING or empty.
	Severity string `json:"severity"`

	// State is NEW or READ.
	State string `json:"state"`

	// CreatedAt is the creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// Summary is the tenant Overview snapshot.
type Summary struct {
	// GeneratedAt is the snapshot time as RFC3339 UTC.
	GeneratedAt string `json:"generated_at"`

	// DeviceTotal is the registered device count.
	DeviceTotal int64 `json:"device_total"`

	// OnlineDevices is the live presence count.
	OnlineDevices int64 `json:"online_devices"`

	// OfflineDevices is DeviceTotal minus OnlineDevices.
	OfflineDevices int64 `json:"offline_devices"`

	// LiveAlarms is the count of ACTIVE and ACK alarms.
	LiveAlarms int64 `json:"live_alarms"`

	// PendingCommands is the count of in-flight command states.
	PendingCommands int64 `json:"pending_commands"`

	// UnreadNotifications is the NEW notification count.
	UnreadNotifications int64 `json:"unread_notifications"`

	// TelemetryLastHour is the telemetry row count in the last hour.
	TelemetryLastHour int64 `json:"telemetry_last_hour"`

	// TelemetryLast24h is the telemetry row count in the last 24 hours.
	TelemetryLast24h int64 `json:"telemetry_last_24h"`

	// TelemetryBuckets contains 5-minute telemetry counts for the last hour.
	TelemetryBuckets []MetricCount `json:"telemetry_buckets"`

	// AlarmSeverities contains live alarm counts grouped by severity.
	AlarmSeverities []MetricCount `json:"alarm_severities"`

	// RecentAlarms contains the most recent live alarms.
	RecentAlarms []RecentAlarm `json:"recent_alarms"`

	// RecentNotifications contains the most recent inbox notifications.
	RecentNotifications []RecentNotification `json:"recent_notifications"`
}

// Service constructs Overview snapshots.
type Service struct {
	pool     *pgxpool.Pool
	presence PresenceCounter
}

// NewService constructs a dashboard service.
func NewService(pool *pgxpool.Pool, presence PresenceCounter) *Service {
	return &Service{pool: pool, presence: presence}
}

// Summary returns one aggregated tenant snapshot.
func (s *Service) Summary(ctx context.Context, tenantID string) (Summary, error) {
	now := time.Now().UTC()
	summary := Summary{
		GeneratedAt:         now.Format(time.RFC3339),
		TelemetryBuckets:    []MetricCount{},
		AlarmSeverities:     []MetricCount{},
		RecentAlarms:        []RecentAlarm{},
		RecentNotifications: []RecentNotification{},
	}

	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM device
		 WHERE tenant_id = $1 AND state <> 'DECOMMISSIONED'),
			(SELECT count(*) FROM alarm
			 WHERE tenant_id = $1 AND state IN ('ACTIVE', 'ACK')),
			(SELECT count(*) FROM command
			 WHERE tenant_id = $1 AND state IN ('PENDING', 'CLAIMED', 'QUEUED')),
			(SELECT count(*) FROM notification
			 WHERE tenant_id = $1 AND state = 'NEW'),
			(SELECT count(*) FROM ts_telemetry
			 WHERE tenant_id = $1 AND time >= now() - interval '1 hour'),
			(SELECT count(*) FROM ts_telemetry
			 WHERE tenant_id = $1 AND time >= now() - interval '24 hours')`,
		tenantID).Scan(
		&summary.DeviceTotal,
		&summary.LiveAlarms,
		&summary.PendingCommands,
		&summary.UnreadNotifications,
		&summary.TelemetryLastHour,
		&summary.TelemetryLast24h,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("load overview counters: %w", err)
	}

	online, err := s.presence.CountOnline(ctx, tenantID)
	if err != nil {
		return Summary{}, fmt.Errorf("count online devices: %w", err)
	}
	summary.OnlineDevices = int64(online)
	summary.OfflineDevices = summary.DeviceTotal - summary.OnlineDevices

	rows, err := s.pool.Query(ctx, `
		WITH buckets AS (
			SELECT generate_series(
				date_bin('5 minutes', now() - interval '55 minutes', TIMESTAMPTZ '2026-01-01'),
				date_bin('5 minutes', now(), TIMESTAMPTZ '2026-01-01'),
				interval '5 minutes'
			) AS bucket
		)
		SELECT b.bucket, count(t.time) AS points
		FROM buckets b
		LEFT JOIN ts_telemetry t
			ON t.tenant_id = $1
			AND t.time >= b.bucket
			AND t.time < b.bucket + interval '5 minutes'
		GROUP BY b.bucket
		ORDER BY b.bucket`, tenantID)
	if err != nil {
		return Summary{}, fmt.Errorf("load telemetry buckets: %w", err)
	}
	for rows.Next() {
		var bucket time.Time
		var count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			rows.Close()
			return Summary{}, fmt.Errorf("scan telemetry bucket: %w", err)
		}
		summary.TelemetryBuckets = append(summary.TelemetryBuckets, MetricCount{
			Label: bucket.Format(time.RFC3339),
			Count: count,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}

	severityRows, err := s.pool.Query(ctx, `
		SELECT severity, count(*)
		FROM alarm
		WHERE tenant_id = $1 AND state IN ('ACTIVE', 'ACK')
		GROUP BY severity
		ORDER BY severity`, tenantID)
	if err != nil {
		return Summary{}, fmt.Errorf("load alarm severities: %w", err)
	}
	for severityRows.Next() {
		var metric MetricCount
		if err := severityRows.Scan(&metric.Label, &metric.Count); err != nil {
			severityRows.Close()
			return Summary{}, fmt.Errorf("scan alarm severity: %w", err)
		}
		summary.AlarmSeverities = append(summary.AlarmSeverities, metric)
	}
	severityRows.Close()
	if err := severityRows.Err(); err != nil {
		return Summary{}, err
	}

	alarmRows, err := s.pool.Query(ctx, `
		SELECT id::text, device_id::text, type, severity, state, started_at
		FROM alarm
		WHERE tenant_id = $1 AND state IN ('ACTIVE', 'ACK')
		ORDER BY started_at DESC
		LIMIT 6`, tenantID)
	if err != nil {
		return Summary{}, fmt.Errorf("load recent alarms: %w", err)
	}
	for alarmRows.Next() {
		var alarm RecentAlarm
		var startedAt time.Time
		if err := alarmRows.Scan(&alarm.ID, &alarm.DeviceID,
			&alarm.Type, &alarm.Severity, &alarm.State, &startedAt); err != nil {
			alarmRows.Close()
			return Summary{}, fmt.Errorf("scan recent alarm: %w", err)
		}
		alarm.StartedAt = startedAt.UTC().Format(time.RFC3339)
		summary.RecentAlarms = append(summary.RecentAlarms, alarm)
	}
	alarmRows.Close()
	if err := alarmRows.Err(); err != nil {
		return Summary{}, err
	}

	notificationRows, err := s.pool.Query(ctx, `
		SELECT id::text, COALESCE(device_id::text, ''), title, body,
			COALESCE(severity, ''), state, created_at
		FROM notification
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT 6`, tenantID)
	if err != nil {
		return Summary{}, fmt.Errorf("load recent notifications: %w", err)
	}
	for notificationRows.Next() {
		var notification RecentNotification
		var createdAt time.Time
		if err := notificationRows.Scan(&notification.ID, &notification.DeviceID,
			&notification.Title, &notification.Body, &notification.Severity,
			&notification.State, &createdAt); err != nil {
			notificationRows.Close()
			return Summary{}, fmt.Errorf("scan recent notification: %w", err)
		}
		notification.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		summary.RecentNotifications = append(summary.RecentNotifications, notification)
	}
	notificationRows.Close()
	if err := notificationRows.Err(); err != nil {
		return Summary{}, err
	}

	return summary, nil
}
