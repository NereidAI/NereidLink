// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package opinvoke implements immediate whole-gateway operation execution
// (ADR-0035): an operator selects a descriptor from the operation catalog,
// fills its parameters, and the request is dispatched synchronously to the
// live gateway through Core NATS with UUID correlation.
//
// Design constraints:
//
//   - Nothing is ever queued: an offline gateway fails immediately with a
//     stored NOT_CONNECTED task. Sub-device commands keep using the
//     command router.
//   - Persisted params carry secret:// references only; the MQTT relay
//     resolves them at the last mile.
//   - The wait is hard-capped at 60 seconds; params and results at 64KB.
//   - Risk comes exclusively from the catalog descriptor; the kernel has
//     no operation-name vocabulary.
//   - Task rows are operational evidence retained 90 days; the chained
//     audit log is the permanent record and never stores params or result
//     content.
package opinvoke

import (
	"encoding/json"
	"errors"
)

// Task status values.
const (
	// StatusPending exists only between row insert and dispatch completion.
	StatusPending = "PENDING"
	// StatusSucceeded means the edge returned a result (or, for a one-way
	// interaction, the ingress wrote the invoke packet).
	StatusSucceeded = "SUCCEEDED"
	// StatusFailed is an edge-reported failure or an infrastructure error.
	StatusFailed = "FAILED"
	// StatusTimeout means no result arrived within the descriptor timeout.
	StatusTimeout = "TIMEOUT"
	// StatusNotConnected means the gateway had no live session at invoke.
	StatusNotConnected = "NOT_CONNECTED"
)

// Limits enforced at the service boundary.
const (
	// MaxParamsBytes bounds one encoded params document.
	MaxParamsBytes = 64 * 1024
	// MaxResultBytes bounds one encoded result document.
	MaxResultBytes = 64 * 1024
	// MaxWait is the hard response-time cap even for a descriptor that
	// claims more (descriptors are validated to 60s already; defense in
	// depth).
	MaxWaitMs = 60000
	// MinWaitMs is the floor for a per-call timeout override; a shorter
	// requested wait is clamped up to it.
	MinWaitMs = 100
)

// Sentinel errors.
var (
	// ErrOperationNotFound means no effective descriptor exists for the key.
	ErrOperationNotFound = errors.New("gateway operation not found")
	// ErrOperationDisabled means the descriptor is disabled for the tenant.
	ErrOperationDisabled = errors.New("gateway operation is disabled")
	// ErrParamsTooLarge means the encoded params exceed the 64KB limit.
	ErrParamsTooLarge = errors.New("operation params exceed 64KB")
	// ErrResultTooLarge means the encoded edge result exceeds the 64KB limit.
	ErrResultTooLarge = errors.New("operation result exceeds 64KB")
	// ErrInvalidParams means the params payload is not a JSON object.
	ErrInvalidParams = errors.New("operation params must be a JSON object")
	// ErrParamsSchema means params violate the descriptor param schema.
	ErrParamsSchema = errors.New("operation params failed schema validation")
	// ErrNotFound means no task exists for the lookup.
	ErrNotFound = errors.New("operation task not found")
)

// Actor identifies the human operator for the task and audit records.
type Actor struct {
	// UserID is the account id.
	UserID string
	// Email is the login name, stored for readable history.
	Email string
}

// InvokeInput is one invocation request from the REST layer.
type InvokeInput struct {
	// TenantID owns the gateway.
	TenantID string
	// GatewayID is the target gateway.
	GatewayID string
	// Operation is the catalog key.
	Operation string
	// Params carries secret:// references where an edge credential is used.
	Params map[string]any
	// TimeoutMs optionally lowers the descriptor default for this call;
	// zero keeps the descriptor default. It can never raise the wait past
	// the descriptor or the 60s hard cap.
	TimeoutMs int
	// Actor is the authenticated operator.
	Actor Actor
}

// Task is one persisted invocation row.
type Task struct {
	// ID is the task UUID, used for correlation on the wire.
	ID string `json:"id"`
	// TenantID owns the gateway.
	TenantID string `json:"tenant_id"`
	// GatewayID is the target gateway.
	GatewayID string `json:"gateway_id"`
	// Operation is the executed catalog key.
	Operation string `json:"operation"`
	// TypeVersion is the descriptor version used.
	TypeVersion string `json:"type_version,omitempty"`
	// Params is the reference-only params document.
	Params map[string]any `json:"params"`
	// Result is the edge-reported result (any JSON document).
	Result json.RawMessage `json:"result,omitempty"`
	// Status is one of the Status* constants.
	Status string `json:"status"`
	// Reason carries an error explanation.
	Reason string `json:"reason,omitempty"`
	// Risk is the descriptor risk level.
	Risk string `json:"risk"`
	// InvokedBy is the operator account id.
	InvokedBy string `json:"invoked_by"`
	// InvokedByEmail is the operator email.
	InvokedByEmail string `json:"invoked_by_email,omitempty"`
	// StartedAt is the invoke time.
	StartedAt string `json:"started_at"`
	// FinishedAt is the completion time.
	FinishedAt string `json:"finished_at,omitempty"`
}

// InvokeFrame is the Core NATS request written to the owning ingress and
// re-encoded by the relay into the gateway MQTT packet.
type InvokeFrame struct {
	// ID is the task id used for correlation.
	ID string `json:"id"`
	// TenantID owns the gateway.
	TenantID string `json:"tenant_id"`
	// GatewayID is the target gateway.
	GatewayID string `json:"gateway_id"`
	// Operation is the catalog key.
	Operation string `json:"operation"`
	// TypeVersion is the descriptor version.
	TypeVersion string `json:"type_version,omitempty"`
	// Params carries the reference-only params; the relay opens references.
	Params map[string]any `json:"params"`
	// TimeoutMs is the per-call wait, never over 60000.
	TimeoutMs int `json:"timeout_ms"`
	// OneWay marks an interaction with no edge result.
	OneWay bool `json:"one_way,omitempty"`
}

// ResultFrame is the edge-reported result carried back through the relay.
type ResultFrame struct {
	// ID is the task id.
	ID string `json:"id"`
	// OK reports edge-level success.
	OK bool `json:"ok"`
	// Result is any JSON document within the 64KB limit.
	Result json.RawMessage `json:"result,omitempty"`
	// Error carries a short edge failure explanation.
	Error string `json:"error,omitempty"`
}
