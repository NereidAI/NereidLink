// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package alarm

import "context"

// CreateInput is the CreateAlarm input.
type CreateInput struct {
	// TenantID owns the device.
	TenantID string

	// DeviceID is the alarm originator.
	DeviceID string

	// RuleID identifies the raising rule node when present.
	RuleID string

	// AlarmType classifies the alarm.
	AlarmType string

	// Severity is the alarm severity.
	Severity string

	// DedupeKey identifies the open alarm.
	DedupeKey string

	// Details is the alarm detail document.
	Details map[string]any

	// Propagate marks propagation.
	Propagate bool

	// EscalationChainID binds the escalation chain applied to this alarm.
	EscalationChainID string
}

// Outcome reports an alarm transition.
type Outcome struct {
	// AlarmID identifies the alarm.
	AlarmID string

	// Created is true for a new alarm, false for an update.
	Created bool

	// Severity is the current severity.
	Severity string

	// Escalated reports a severity increase.
	Escalated bool

	// State is the lifecycle state.
	State string
}

// Raiser is the interface consumed by the rule Create/Clear Alarm nodes.
type Raiser interface {
	CreateAlarm(ctx context.Context, in CreateInput) (Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (Outcome, error)
}
