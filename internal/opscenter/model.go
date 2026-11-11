// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package opscenter implements the ADR-0023 cleanup order flow.
// Operators open destructive data/cleanup orders from a loopback CLI
// listener; the single super administrator reviews the full payload on
// the business API web console and confirms it before the registered
// executor runs. Unconfirmed orders expire after a bounded validity
// window.
package opscenter

import (
	"encoding/json"
	"time"
)

// Lifecycle and channel constants. They are stored verbatim in the
// ops_order table and duplicated by its CHECK constraints.
const (
	// StatusPending awaits review.
	StatusPending = "PENDING"
	// StatusApproved passed review and may execute exactly once.
	StatusApproved = "APPROVED"
	// StatusRejected was refused by the reviewer.
	StatusRejected = "REJECTED"
	// StatusExecuted means the approved action has run.
	StatusExecuted = "EXECUTED"
	// StatusExpired reached its validity window while pending.
	StatusExpired = "EXPIRED"
	// StatusCancelled was withdrawn by its initiator.
	StatusCancelled = "CANCELLED"

	// ChannelWeb is the browser console.
	ChannelWeb = "WEB"
	// ChannelCLI is the ops command-line client.
	ChannelCLI = "CLI"
)

// High-risk action catalog. Only these keys may be put on an order;
// the execution stage maps each one to a concrete guarded handler.
// The catalog is limited to destructive data/cleanup operations that
// operators run from the CLI; ordinary configuration stays on the web.
const (
	// ActionTelemetryPurge deletes stored telemetry before a cutoff.
	ActionTelemetryPurge = "telemetry.purge"
	// ActionTenantDeprovision deregisters a tenant permanently.
	ActionTenantDeprovision = "tenant.deprovision"
	// ActionRetentionShorten lowers a retention window.
	ActionRetentionShorten = "retention.shorten"
	// ActionForceDisconnect forcibly closes a node/device connection.
	ActionForceDisconnect = "node.force-disconnect"
	// ActionLicenseReplace installs a new license artifact.
	ActionLicenseReplace = "license.replace"
	// ActionQuarantineRelease lifts a quarantine state.
	ActionQuarantineRelease = "quarantine.release"
)

// allowedActions is the whitelist enforced at order creation, so the
// table cannot be used to smuggle arbitrary operations.
var allowedActions = map[string]bool{
	ActionTelemetryPurge:    true,
	ActionTenantDeprovision: true,
	ActionRetentionShorten:  true,
	ActionForceDisconnect:   true,
	ActionLicenseReplace:    true,
	ActionQuarantineRelease: true,
}

// Order is one signed operation order with its full review state.
type Order struct {
	// ID identifies the order.
	ID string `json:"id"`

	// TenantID scopes the action; empty means platform-global.
	TenantID string `json:"tenant_id,omitempty"`

	// Action is one of the catalog constants.
	Action string `json:"action"`

	// Summary is the human-readable description for the reviewer.
	Summary string `json:"summary"`

	// Payload is the full request content under review.
	Payload json.RawMessage `json:"payload"`

	// ContentHash binds the order to action, scope and payload.
	ContentHash string `json:"content_hash"`

	// InitiatorID is the account that opened the order.
	InitiatorID string `json:"initiator_id,omitempty"`

	// InitiatorName is the initiator email.
	InitiatorName string `json:"initiator_name"`

	// InitiatorChannel is WEB or CLI.
	InitiatorChannel string `json:"initiator_channel"`

	// Status is the current lifecycle state.
	Status string `json:"status"`

	// ReviewerID is the account that decided the order.
	ReviewerID string `json:"reviewer_id,omitempty"`

	// ReviewerName is the reviewer email.
	ReviewerName string `json:"reviewer_name"`

	// ReviewerChannel must differ from the initiator channel.
	ReviewerChannel string `json:"reviewer_channel"`

	// ReviewReason explains a rejection or approval note.
	ReviewReason string `json:"review_reason"`

	// ReviewedAt is the decision time as RFC3339 UTC.
	ReviewedAt string `json:"reviewed_at,omitempty"`

	// ExecutedAt is when the approved action ran, RFC3339 UTC.
	ExecutedAt string `json:"executed_at,omitempty"`

	// ExecResult captures the outcome of the executed action.
	ExecResult json.RawMessage `json:"exec_result"`

	// ExpiresAt ends the validity window, RFC3339 UTC.
	ExpiresAt string `json:"expires_at"`

	// CreatedAt is the order creation time, RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// CreateOrderInput carries the initiator request. TTL zero selects the
// default validity window.
type CreateOrderInput struct {
	// TenantID may be empty for platform-global orders.
	TenantID string

	// Action must be in the whitelist.
	Action string

	// Summary is optional but recommended for review clarity.
	Summary string

	// Payload must be a JSON object; empty becomes {}.
	Payload json.RawMessage

	// InitiatorID is optional: the CLI operator is usually an OS
	// user rather than an application account.
	InitiatorID string

	// InitiatorName records the operator identity (OS user name or
	// explicit --operator) for the reviewer and the audit trail.
	InitiatorName string

	// InitiatorChannel is always CLI; orders cannot be opened on web.
	InitiatorChannel string

	// TTL bounds validity; zero uses the default.
	TTL time.Duration
}
