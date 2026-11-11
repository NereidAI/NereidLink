// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package secretbinding stores the desired-state bindings that deliver
// tenant vault secrets to a gateway (ADR-0034, migration 0071). A binding
// binds one secret name to one gateway with generic target selectors,
// tracks a monotonic revision and a delivery status.
//
// The desired row is the only state the platform mutates offline:
// successive edits merge into one row and bump revision, so delivery
// always sends the latest desired content. Withdrawal moves a row through
// REMOVING to REMOVED; the edge reports removal.
package secretbinding

import (
	"errors"
	"time"
)

// Status values.
const (
	StatusPending    = "PENDING"
	StatusDispatched = "DISPATCHED"
	StatusApplied    = "APPLIED"
	StatusFailed     = "FAILED"
	StatusRemoving   = "REMOVING"
	StatusRemoved    = "REMOVED"
)

// Alarm types raised by the delivery sweep (mirror connector alarms).
const (
	// AlarmTypePending is the WARNING not-applied alarm for a stuck binding.
	AlarmTypePending = "GATEWAY_SECRET_PENDING"
	// AlarmTypeFailed is the MAJOR/CRITICAL delivery-failure alarm.
	AlarmTypeFailed = "GATEWAY_SECRET_FAILED"
)

const (
	// dedupePendingPrefix starts the pending-alarm dedupe key.
	dedupePendingPrefix = "gateway-secret-pending:"
	// dedupeFailedPrefix starts the failed-alarm dedupe key.
	dedupeFailedPrefix = "gateway-secret-failed:"
)

// DedupePendingKey returns the not-applied alarm dedupe key for a binding.
func DedupePendingKey(secretName string) string { return dedupePendingPrefix + secretName }

// DedupeFailedKey returns the FAILED alarm dedupe key for a binding.
func DedupeFailedKey(secretName string) string { return dedupeFailedPrefix + secretName }

// SubjectEventPrefix is the Core NATS root for user-plane secret delivery
// notifications: "nl.secret.event.<tenant>" (tenant on token 4), separate
// from the desired push channel.
const SubjectEventPrefix = "nl.secret.event"

// EventWildcard matches every delivery event subject.
const EventWildcard = "nl.secret.event.>"

// Delivery event kinds. They are change notifications only: the console
// re-fetches the gateway's binding list rather than mutating from the frame.
const (
	// EventDesired signals a desired binding create/update was pushed.
	EventDesired = "desired"
	// EventWithdrawn signals a binding entered REMOVING.
	EventWithdrawn = "withdrawn"
	// EventStatus signals an edge-reported status was applied.
	EventStatus = "status"
)

// EventFrame is one user-plane secret delivery notification.
type EventFrame struct {
	// Kind is one of the Event* constants.
	Kind string `json:"kind"`
	// TenantID scopes the frame; it is also encoded in the subject and must
	// never be trusted from the payload for fanout boundaries.
	TenantID string `json:"tenant_id"`
	// GatewayID identifies the gateway whose binding set changed.
	GatewayID string `json:"gateway_id"`
	// SecretName identifies the affected binding; empty for a full-set event.
	SecretName string `json:"secret_name,omitempty"`
	// Status is the edge-reported state for EventStatus.
	Status string `json:"status,omitempty"`
	// Revision is the desired revision relevant to the event.
	Revision int64 `json:"revision,omitempty"`
}

// Selector is one generic target selector on a binding.
type Selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Binding is one gateway/secret binding row.
type Binding struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	GatewayID    string     `json:"gateway_id"`
	SecretName   string     `json:"secret_name"`
	SecretType   string     `json:"secret_type"`
	TypeVersion  string     `json:"type_version"`
	Selectors    []Selector `json:"selectors"`
	Revision     int64      `json:"revision"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
	RemovedAt    *time.Time `json:"removed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// DesiredChange carries a create/update desired binding request.
type DesiredChange struct {
	SecretName  string
	SecretType  string
	TypeVersion string
	Selectors   []Selector
}

// Report is one edge-reported status.
type Report struct {
	SecretName string
	Status     string
	Reason     string
}

// Reference is one gateway still holding a delivered copy of a vault
// secret. It backs the deletion-protection prompt: operators see which
// gateways block a normal delete and can withdraw from them first.
type Reference struct {
	// GatewayID is the gateway device id.
	GatewayID string `json:"gateway_id"`
	// GatewayName is the gateway's canonical name for display.
	GatewayName string `json:"gateway_name"`
	// Status is the binding's current non-removed status.
	Status string `json:"status"`
}

// StatusEntry is one row of the edge status uplink.
type StatusEntry struct {
	// Name identifies the secret binding.
	Name string `json:"name"`

	// Revision optionally echoes the delivered revision the report refers
	// to. Stale revisions are not rejected; the binding is full-state.
	Revision int64 `json:"revision,omitempty"`

	// Status is one of APPLIED / FAILED / REMOVED.
	Status string `json:"status"`

	// Reason carries a short, transport-safe failure explanation.
	Reason string `json:"reason,omitempty"`
}

// StatusFrame is the edge -> platform uplink on
// v1/gateway/secrets/status: one or more reports in a single publish.
type StatusFrame struct {
	// Reports contains the per-secret states.
	Reports []StatusEntry `json:"reports"`
}

// Sentinel errors.
var (
	// ErrNotFound means no binding exists for the key.
	ErrNotFound = errors.New("gateway secret binding not found")
	// ErrInvalidSelector means a selector row is malformed.
	ErrInvalidSelector = errors.New("invalid secret selector")
	// ErrEmptySecret means no secret name was given.
	ErrEmptySecret = errors.New("secret name is required")
	// ErrUnknownStatus means the edge sent a status value NL does not
	// recognize as an uplink state.
	ErrUnknownStatus = errors.New("unknown secret status")
	// ErrSecretNotFound means the named secret does not exist in the
	// tenant vault.
	ErrSecretNotFound = errors.New("secret not found")
	// ErrSecretNotDeliverable means the secret is not a typed document and
	// therefore cannot be delivered as a credential document.
	ErrSecretNotDeliverable = errors.New("secret is not a typed document")
)
