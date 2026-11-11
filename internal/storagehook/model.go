// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package storagehook implements the pre-storage user hook pipeline from
// ADR-0015. Hooks execute in the processing plane: the telemetry writer
// consumes an Envelope from JetStream, applies every enabled tenant hook in
// order, and only then buffers the point for TimescaleDB.
//
// The MVP ships three hook mechanisms:
//
//   - expression: a sandboxed JavaScript (goja) snippet that overlays point
//     fields or drops the point.
//   - webhook: an external HTTP callback receiving the point as JSON and
//     returning optional field overlays; this keeps heavy processing out
//     of the platform while preserving timeout and circuit-breaker bounds.
//   - wasm: a WebAssembly module (wazero, WASI) run in-process under hard
//     CPU/memory/output/time budgets. The point is fed as JSON on stdin and
//     an optional overlay object is read from stdout, giving medium-weight
//     logic deterministic isolation without an outbound network dependency.
//
// gRPC callbacks and the external L3 runner are deferred to later releases.
package storagehook

import "context"

// DLQWriter persists one point under the dlq failure policy. It is
// implemented by *Store.
type DLQWriter interface {
	// WriteDLQ stores the rejected point with the failing hook and reason.
	WriteDLQ(ctx context.Context, point Point, hookName, reason string) error
}

const (
	// TypeExpression marks a JavaScript expression hook.
	TypeExpression = "expression"

	// TypeWebhook marks an external HTTP callback hook.
	TypeWebhook = "webhook"

	// TypeWasm marks a wazero-sandboxed WebAssembly hook.
	TypeWasm = "wasm"
)

const (
	// PolicyPassthrough keeps the unmodified point when a hook fails.
	PolicyPassthrough = "passthrough"

	// PolicyAbort drops the point when a hook fails or requests a drop.
	PolicyAbort = "abort"

	// PolicyDLQ writes the point to telemetry_dlq when a hook fails.
	PolicyDLQ = "dlq"
)

// Hook is one resolved ordered hook configuration.
type Hook struct {
	// ID is the globally unique hook identifier.
	ID string

	// TenantID owns the hook.
	TenantID string

	// Name is the unique tenant-scoped hook name, used in diagnostics.
	Name string

	// Order determines ascending execution order.
	Order int

	// Type selects the hook mechanism.
	Type string

	// Keys limits the hook to measurement keys; empty means every key.
	Keys []string

	// Script is the JavaScript source for expression hooks.
	Script string

	// URL is the callback target for webhook hooks.
	URL string

	// Module is the decoded WebAssembly binary for wasm hooks.
	Module []byte

	// Timeout bounds one hook execution.
	TimeoutMS int

	// FailurePolicy selects behavior after an execution failure.
	FailurePolicy string
}

// Point is the mutable telemetry representation passed through hooks.
type Point struct {
	// TenantID owns the originating device.
	TenantID string

	// DeviceID is the originating device.
	DeviceID string

	// MeasurementKey identifies the measurement.
	MeasurementKey string

	// VDouble is the numeric engineering value.
	VDouble float64

	// Quality is the data quality marker.
	Quality string

	// Unit is the engineering unit.
	Unit string

	// RawDouble is the original device-reported value.
	RawDouble float64
}

// asMap renders the point for expression binding or webhook serialization.
func (p Point) asMap() map[string]any {
	return map[string]any{
		"tenant_id":       p.TenantID,
		"device_id":       p.DeviceID,
		"measurement_key": p.MeasurementKey,
		"v_double":        p.VDouble,
		"quality":         p.Quality,
		"unit":            p.Unit,
		"raw_value":       p.RawDouble,
	}
}

// Decision is the pipeline result for one point.
type Decision struct {
	// Drop reports that the point must not be persisted.
	Drop bool

	// ToDLQ reports that the point must be written to the DLQ instead.
	ToDLQ bool

	// HookName names the hook producing the decision when not a pass.
	HookName string

	// Reason explains a drop or DLQ decision.
	Reason string
}
