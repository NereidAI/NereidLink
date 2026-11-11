// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package escalation implements alarm escalation chains (ADR-0019). A
// chain is a tenant-scoped, ordered list of steps. Each step waits a
// delay measured from the alarm start time and then notifies the holders
// of a chosen tenant role. A background runner materializes per-alarm
// firing state, cancels pending steps once the alarm is acknowledged or
// cleared according to step flags, and fires due steps exactly once.
package escalation

import (
	"context"
	"errors"
)

// State values persisted in alarm_escalation_state.
const (
	// StatePending waits for its fire time.
	StatePending = "PENDING"

	// StateFired has been delivered.
	StateFired = "FIRED"

	// StateCanceled was stopped by ack/clear before firing.
	StateCanceled = "CANCELED"
)

// ErrNotFound signals a missing chain in the tenant.
var ErrNotFound = errors.New("escalation chain not found")

// ErrInvalid signals a validation failure; the REST layer maps it to 400.
var ErrInvalid = errors.New("invalid escalation chain")

// allowedRoles lists the tenant roles a step may target. Platform admin
// and service accounts are not valid targets.
var allowedRoles = map[string]bool{
	"TENANT_ADMIN":  true,
	"ENGINEER":      true,
	"CUSTOMER_USER": true,
	"READ_ONLY":     true,
}

// Step is one ordered escalation step.
type Step struct {
	// ID is assigned on persistence.
	ID string `json:"step_id,omitempty"`

	// Order is the zero-based position within the chain.
	Order int `json:"order"`

	// DelaySeconds is measured from the alarm start time.
	DelaySeconds int64 `json:"delay_seconds"`

	// TargetRole is the tenant role notified by this step.
	TargetRole string `json:"target_role"`

	// TargetGroupID optionally narrows the step to role holders whose
	// RBAC data scope covers this device group's subtree. Empty means
	// every holder of TargetRole in the tenant.
	TargetGroupID string `json:"target_group_id,omitempty"`

	// StopOnAck cancels the step if still pending when the alarm is acked.
	StopOnAck bool `json:"stop_on_ack"`

	// StopOnClear cancels the step if still pending when the alarm clears.
	StopOnClear bool `json:"stop_on_clear"`
}

// Chain is an escalation chain with its ordered steps.
type Chain struct {
	// ID identifies the chain.
	ID string `json:"chain_id"`

	// TenantID owns the chain.
	TenantID string `json:"tenant_id"`

	// Name is unique within the tenant.
	Name string `json:"name"`

	// Enabled turns escalation on or off without deleting the chain.
	Enabled bool `json:"enabled"`

	// RepeatIntervalSeconds re-notifies RepeatTargetRole at this cadence
	// while a bound alarm stays unacknowledged. 0 disables reminders.
	RepeatIntervalSeconds int64 `json:"repeat_interval_seconds"`

	// RepeatTargetRole is the role reminded while the alarm is ACTIVE.
	RepeatTargetRole string `json:"repeat_target_role,omitempty"`

	// RepeatTargetGroupID optionally narrows reminders to role holders
	// whose RBAC data scope covers this device group's subtree.
	RepeatTargetGroupID string `json:"repeat_target_group_id,omitempty"`

	// Steps are ordered ascending by Order.
	Steps []Step `json:"steps"`

	// CreatedAt is the creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// UpdatedAt is the last update time as RFC3339 UTC.
	UpdatedAt string `json:"updated_at"`
}

// StepInput carries one step on create/update. Order is derived from the
// slice position, not from the client value.
type StepInput struct {
	// DelaySeconds is measured from the alarm start time.
	DelaySeconds int64 `json:"delay_seconds"`

	// TargetRole is the tenant role notified by this step.
	TargetRole string `json:"target_role"`

	// TargetGroupID optionally narrows the step to role holders whose
	// RBAC data scope covers the referenced device group's subtree.
	TargetGroupID string `json:"target_group_id,omitempty"`

	// StopOnAck cancels the step when the alarm is acked.
	StopOnAck bool `json:"stop_on_ack"`

	// StopOnClear cancels the step when the alarm clears.
	StopOnClear bool `json:"stop_on_clear"`
}

// SaveInput carries chain fields for create or full replace.
type SaveInput struct {
	// Name is unique within the tenant.
	Name string `json:"name"`

	// Enabled defaults to true on create when nil.
	Enabled *bool `json:"enabled,omitempty"`

	// RepeatIntervalSeconds re-notifies RepeatTargetRole at this cadence
	// while a bound alarm stays unacknowledged. 0 disables reminders.
	RepeatIntervalSeconds int64 `json:"repeat_interval_seconds"`

	// RepeatTargetRole is the reminded role; required when the repeat
	// interval is positive.
	RepeatTargetRole string `json:"repeat_target_role,omitempty"`

	// RepeatTargetGroupID optionally narrows reminders to role holders
	// whose RBAC data scope covers the referenced device group's subtree.
	RepeatTargetGroupID string `json:"repeat_target_group_id,omitempty"`

	// Steps replace the existing step list on update.
	Steps []StepInput `json:"steps"`
}

// SendInput is one escalation delivery request resolved by the Sender.
type SendInput struct {
	// Recipients lists target email addresses.
	Recipients []string

	// Subject is the notification title.
	Subject string

	// Body is the rendered text.
	Body string

	// Severity mirrors the alarm severity.
	Severity string
}

// Sender delivers one fired escalation to external channels. It is
// implemented by an adapter in the cmd package using notify.Config.
type Sender interface {
	// Send delivers a webhook and/or email message. Delivery failures
	// for individual channels should be returned as an error.
	Send(ctx context.Context, in SendInput) error
}
