// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// constants.go defines alarm severity, state, subject and event wire values.

package alarm

// Severity values. They are wire values, defined once.
const (
	// SeverityCritical is the highest alarm severity.
	SeverityCritical = "CRITICAL"

	// SeverityMajor is a serious alarm.
	SeverityMajor = "MAJOR"

	// SeverityMinor is a lower-grade alarm.
	SeverityMinor = "MINOR"

	// SeverityWarning is an early warning.
	SeverityWarning = "WARNING"
)

// Lifecycle states.
const (
	// StateActive is a live, unacknowledged alarm.
	StateActive = "ACTIVE"

	// StateAck is a live, acknowledged alarm.
	StateAck = "ACK"

	// StateCleared is a terminal cleared alarm.
	StateCleared = "CLEARED"
)

// SubjectPrefix is the Core NATS alarm fanout root.
const SubjectPrefix = "nl.alarm"

// Wildcard matches every alarm subject for hub and notifier subscriptions.
const Wildcard = SubjectPrefix + ".*.>"

// Event is the JSON payload published on alarm state transitions.
type Event struct {
	// AlarmID identifies the alarm.
	AlarmID string `json:"alarm_id"`

	// TenantID owns the alarm.
	TenantID string `json:"tenant_id"`

	// DeviceID is the alarm originator.
	DeviceID string `json:"device_id"`

	// Type classifies the alarm.
	Type string `json:"alarm_type"`

	// Severity is the current severity.
	Severity string `json:"severity"`

	// State is the lifecycle state.
	State string `json:"state"`

	// DedupeKey identifies the open alarm for notification frequency
	// control.
	DedupeKey string `json:"dedupe_key"`

	// Created is true when this event opened a new alarm.
	Created bool `json:"created"`

	// Escalated reports a severity increase on this event.
	Escalated bool `json:"escalated"`

	// Details carries the alarm detail document.
	Details map[string]any `json:"details,omitempty"`

	// StartedAt is the trigger time as RFC3339 UTC.
	StartedAt string `json:"started_at"`

	// ClearedAt is set on clear as RFC3339 UTC.
	ClearedAt string `json:"cleared_at,omitempty"`
}
