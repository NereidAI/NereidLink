// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package probe implements on-demand gateway probing (ADR-0026 section 9).
// An application posts the devices it wants an immediate reachability/read
// check for; the platform groups them by owning gateway, sends one aggregated
// downlink frame per gateway on v1/gateway/probe, and correlates the
// per-device frames the gateway returns on v1/gateway/probe/response.
//
// The platform carries no probe semantics: the requested mode
// (cached/fresh/strict) and the device result are opaque to the kernel and
// passed through verbatim. Probes are realtime-only (a request for an
// unreachable gateway fails its devices immediately, nothing is queued), the
// work is asynchronous (POST returns 202 + task id; results arrive by poll or
// WebSocket), and results are retained for 24 hours.
package probe

import (
	"encoding/json"
	"time"
)

// Mode values carried in a probe request. They are validated and forwarded
// but not interpreted by the platform.
const (
	ModeCached = "cached"
	ModeFresh  = "fresh"
	ModeStrict = "strict"
)

// Default limits and timing. All are overridable via config ProbeSection.
const (
	// DefaultTimeoutMS is the per-device probe hint given to the edge when
	// the request omits one.
	DefaultTimeoutMS = 10_000

	// DefaultTaskDeadline bounds how long a task waits for outstanding
	// device responses before timing them out.
	DefaultTaskDeadline = 60 * time.Second

	// deadlineSlack is added to the per-device hint when computing a task's
	// effective deadline, allowing one network round-trip past the edge
	// timeout hint.
	deadlineSlack = 2 * time.Second

	// DefaultMaxDevices bounds one task's device count.
	DefaultMaxDevices = 100

	// DefaultCreateRate is the minimum accepted interval between task
	// creations for one principal (token-bucket refill, effectively 1 QPS).
	DefaultCreateRate = time.Second

	// DefaultMaxConcurrent bounds unfinished tasks per principal.
	DefaultMaxConcurrent = 5

	// DefaultCooldown is the minimum interval between dispatches that
	// contain the same device, per node.
	DefaultCooldown = 10 * time.Second

	// DefaultRetention is how long finished task results are kept.
	DefaultRetention = 24 * time.Hour

	// DefaultSweepInterval paces the retention/deadline sweeper.
	DefaultSweepInterval = time.Minute
)

// Task lifecycle states.
const (
	TaskStatePending   = "pending"
	TaskStateRunning   = "running"
	TaskStateCompleted = "completed"
	TaskStatePartial   = "partial"
	TaskStateFailed    = "failed"
)

// Item lifecycle states.
const (
	ItemStatePending   = "pending"
	ItemStateRunning   = "running"
	ItemStateSucceeded = "succeeded"
	ItemStateFailed    = "failed"
)

// Per-device delivery outcomes reported over Core NATS by the ingress relay.
const (
	// OutcomeOK carries a device probe result from the gateway.
	OutcomeOK = "ok"
	// OutcomeTimeout marks a device whose result did not arrive in time.
	OutcomeTimeout = "timeout"
	// OutcomeNotConnected marks a device whose gateway is offline/unknown.
	OutcomeNotConnected = "not_connected"
	// OutcomeTransportError marks a failed write to the gateway client.
	OutcomeTransportError = "transport_error"
)

// TaskFilter narrows a task list. All fields are optional; zero values
// apply no constraint.
type TaskFilter struct {
	// State matches the aggregate task state (pending/running/completed/
	// partial/failed).
	State string

	// Mode matches the requested probe mode (cached/fresh/strict).
	Mode string

	// Query matches a task by the canonical name or alias of its gateway
	// or any probed device (case-insensitive partial).
	Query string
}

// Task is one on-demand probe request and its aggregate progress.
type Task struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	CreatedBy string `json:"created_by,omitempty"`
	Mode      string `json:"mode"`
	TimeoutMS int64  `json:"timeout_ms"`
	// GatewayID is the gateway the task was created against. The creation
	// path is single-gateway, so it is read from one task item; empty for
	// a task with no items.
	GatewayID      string     `json:"gateway_id,omitempty"`
	State          string     `json:"state"`
	TotalCount     int        `json:"total_count"`
	SucceededCount int        `json:"succeeded_count"`
	FailedCount    int        `json:"failed_count"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// Item is the per-device member of a probe task.
type Item struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	TenantID   string     `json:"tenant_id"`
	GatewayID  string     `json:"gateway_id"`
	DeviceID   string     `json:"device_id"`
	State      string     `json:"state"`
	Result     []byte     `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Request is the validated REST input for creating a probe task.
type Request struct {
	// Devices is the explicit list of target device UUIDs.
	Devices []string

	// Mode is cached/fresh/strict; empty defaults to fresh.
	Mode string

	// TimeoutMS overrides the per-device probe hint; 0 uses the default.
	TimeoutMS int64
}

// TaskEvent is the progress frame published over Core NATS for WebSocket
// fan-out. Type is "task" (aggregate state) or "item" (one device outcome).
type TaskEvent struct {
	Type string `json:"type"`
	Task Task   `json:"task"`
	Item *Item  `json:"item,omitempty"`
}

const (
	eventTypeTask = "task"
	eventTypeItem = "item"
)

// DispatchFrame is the Core NATS request from the manager to the ingress node
// owning a gateway connection, and the JSON body written to
// v1/gateway/probe for that one client. One frame covers every device of the
// task that shares the gateway.
type DispatchFrame struct {
	ProbeID   string   `json:"probe_id"`
	TenantID  string   `json:"tenant_id"`
	GatewayID string   `json:"gateway_id"`
	Devices   []string `json:"devices"`
	Mode      string   `json:"mode"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// ResponseFrame is the uplink body on v1/gateway/probe/response, echoed by
// the gateway per probed device. Result is opaque JSON.
type ResponseFrame struct {
	ProbeID string          `json:"probe_id"`
	Device  string          `json:"device"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// BulkResponseFrame is the optional batched uplink body on
// v1/gateway/probe/response: a gateway may answer several probed devices in a
// single publish. A frame carrying a top-level device/result is the legacy
// per-device form; when responses is present the top-level device fields are
// ignored and every entry is applied as if published individually.
type BulkResponseFrame struct {
	ProbeID   string          `json:"probe_id"`
	Responses []ResponseFrame `json:"responses,omitempty"`
}

// Event is the per-device outcome the ingress relay publishes on the Core NATS
// event subject (nl.probe.events.<tenant>.<device>) and the manager consumes.
// It is also used for delivery-ack and timeout outcomes.
type Event struct {
	ProbeID   string          `json:"probe_id"`
	TenantID  string          `json:"tenant_id"`
	GatewayID string          `json:"gateway_id"`
	DeviceID  string          `json:"device_id"`
	Outcome   string          `json:"outcome"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// DispatchSubjectPrefix is nl.probe.dispatch.<ingress-id>: a request to the
// one ingress node owning the gateway MQTT connection.
const DispatchSubjectPrefix = "nl.probe.dispatch."

// EventsSubjectPrefix is nl.probe.events.<tenant>.<device>.
const EventsSubjectPrefix = "nl.probe.events."

// TaskSubjectPrefix is nl.probe.task.<tenant>: WebSocket progress frames.
const TaskSubjectPrefix = "nl.probe.task."

// EventsWildcard subscribes the manager to every per-device outcome.
const EventsWildcard = EventsSubjectPrefix + "*.*"

// dispatchSubject addresses one ingress node.
func dispatchSubject(ingressID string) string {
	return DispatchSubjectPrefix + ingressID
}

// taskSubject addresses one tenant's progress channel.
func taskSubject(tenantID string) string {
	return TaskSubjectPrefix + tenantID
}
