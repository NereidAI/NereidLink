// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package audit implements the append-only audit log and its
// tamper-evident hash chain required by ADR-0029.
//
// Every event stores the SHA-256 over its canonical content plus the
// previous event hash. Appends serialize cluster-wide through a
// transaction advisory lock; rows are never updated or deleted. A
// background sealer anchors each completed day and Verify recomputes
// the whole chain, so any insertion, modification or deletion is
// detectable by the doctor check.
package audit

// Event type catalog from ADR-0029. The set is version-fixed; plugins
// declare their own event types instead of reusing generic strings.
const (
	// EventAuthLogin is a successful interactive login.
	EventAuthLogin = "AUTH_LOGIN"

	// EventAuthLoginFailed is a rejected login attempt.
	EventAuthLoginFailed = "AUTH_LOGIN_FAILED"

	// EventAuthLogout is a session revocation.
	EventAuthLogout = "AUTH_LOGOUT"

	// EventConfigChange is a profile/rule/hook configuration change.
	EventConfigChange = "CONFIG_CHANGE"

	// EventCommand is a command dispatch.
	EventCommand = "COMMAND_DISPATCH"

	// EventCredential is device credential management.
	EventCredential = "CREDENTIAL_MANAGE" //nolint:gosec // event type name, not a secret

	// EventPermission is a membership/role change.
	EventPermission = "PERMISSION_CHANGE"

	// EventHighRisk is an ADR-0023 high-risk operation order.
	EventHighRisk = "HIGH_RISK_OPERATION"

	// EventTenantLifecycle is provisioning/suspension/license changes.
	EventTenantLifecycle = "TENANT_LIFECYCLE"

	// EventStepUp is a step-up challenge start, verification, consumption
	// or a rejected attempt (failure/replay/expiry), distinguished by action.
	EventStepUp = "STEP_UP"

	// EventGatewayOperation is an immediate whole-gateway operation
	// invocation (ADR-0035), distinguished by risk and result status.
	EventGatewayOperation = "GATEWAY_OPERATION"
)

// Event is one stored audit row.
type Event struct {
	// Seq is the chain position.
	Seq int64 `json:"seq"`

	// Time is the event time as RFC3339 UTC.
	Time string `json:"time"`

	// TenantID is the tenant in scope; empty for platform events.
	TenantID string `json:"tenant_id,omitempty"`

	// ActorID is the user that caused the event.
	ActorID string `json:"actor_id,omitempty"`

	// ActorName is the email of the actor.
	ActorName string `json:"actor_name"`

	// EventType is one of the catalog constants.
	EventType string `json:"event_type"`

	// Action describes the verb, e.g. "create", "update", "ack".
	Action string `json:"action"`

	// ResourceType names the affected resource family.
	ResourceType string `json:"resource_type"`

	// ResourceID identifies the affected resource.
	ResourceID string `json:"resource_id"`

	// Details carries non-sensitive structured context.
	Details map[string]any `json:"details"`

	// IP is the request origin address.
	IP string `json:"ip"`

	// PrevHash links the previous chain entry.
	PrevHash string `json:"prev_hash"`

	// Hash seals this row.
	Hash string `json:"hash"`
}

// Input is an event before chain enrichment.
type Input struct {
	// TenantID may be empty.
	TenantID string

	// ActorID may be empty for system events.
	ActorID string

	// ActorName helps read the log without joining identity tables.
	ActorName string

	// EventType is one of the catalog constants.
	EventType string

	// Action describes the verb.
	Action string

	// ResourceType names the resource family.
	ResourceType string

	// ResourceID identifies the resource.
	ResourceID string

	// Details are optional structured context.
	Details map[string]any

	// IP records the request origin.
	IP string
}
