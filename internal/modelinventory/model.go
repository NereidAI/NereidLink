// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package modelinventory implements the cloud Phase A view of edge model
// inference (ADR-0039). No model bytes pass through NereidLink: a gateway
// reports its local model inventory in the optional full-replacement
// manifest "models" segment, and this package stores/reconciles that view,
// serves it to the console, raises the model/runtime alarm families, and
// marks the inventory stale when the gateway's gw-manifest lease expires.
//
// The package is deliberately transport-independent and vendor-neutral: it
// stores every reported field verbatim, never interprets labels or family
// semantics, and never initiates an inference call. The generic
// edge-inference connector (ADR-0039 §3.3) needs no special-casing here.
package modelinventory

import (
	"encoding/json"
	"strings"
	"time"
)

// Edge-reported model lifecycle states (manifest models segment). They must
// stay byte-identical to the edge SDK device.ModelState* constants.
const (
	StateExtracting = "EXTRACTING"
	StateValidating = "VALIDATING"
	StateReady      = "READY"
	StateFailed     = "FAILED"
	StateRemoved    = "REMOVED"
)

// Runtime states reported in the device-shadow reported "inference" block
// (ADR-0039 §3.5). They are the runtime plane, distinct from model state.
const (
	RuntimeOnline     = "ONLINE"
	RuntimeDegraded   = "DEGRADED"
	RuntimeOffline    = "OFFLINE"
	RuntimeUnknown    = "UNKNOWN"
	runtimeStaleLimit = 3 // now-checked_at > 3*probe_interval => stale
)

// Alarm types for the model and runtime planes. The binding plane reuses the
// connector alarm families (GATEWAY_CONNECTOR_FAILED / _DEGRADED). Each is a
// distinct, deduplicated ADR-0019 event with an auto-derived recovery.
const (
	// AlarmTypeModelFailed opens per model when the edge reports FAILED; it
	// is cleared when the model leaves FAILED or is pruned.
	AlarmTypeModelFailed = "GATEWAY_MODEL_FAILED"
	// AlarmTypeModelStale opens once per gateway while the manifest lease is
	// expired (inventory stale) and clears on the next successful models
	// reconciliation.
	AlarmTypeModelStale = "GATEWAY_MODEL_STALE"
	// AlarmTypeRuntime opens while the inference runtime is reported OFFLINE
	// or its shadow health block is silent past 3 probe intervals.
	AlarmTypeRuntime = "GATEWAY_INFERENCE_RUNTIME"
)

// Dedupe-key prefixes; suffixes identify the affected subject.
const (
	dedupeModelFailedPrefix = "gateway-model-failed:"
	dedupeModelStalePrefix  = "gateway-model-stale:"
	dedupeRuntimePrefix     = "gateway-inference-runtime:"
)

// DedupeModelFailedKey returns the per-model FAILED alarm dedupe key.
func DedupeModelFailedKey(gatewayID, modelID, version string) string {
	return dedupeModelFailedPrefix + gatewayID + ":" + modelID + ":" + version
}

// DedupeModelStaleKey returns the per-gateway inventory-stale alarm key.
func DedupeModelStaleKey(gatewayID string) string {
	return dedupeModelStalePrefix + gatewayID
}

// DedupeRuntimeKey returns the per-gateway runtime alarm key.
func DedupeRuntimeKey(gatewayID string) string {
	return dedupeRuntimePrefix + gatewayID
}

// Model is one model present on one gateway.
type Model struct {
	TenantID  string `json:"-"`
	GatewayID string `json:"-"`

	// ModelID/Version form the inventory key.
	ModelID string `json:"id"`
	Version string `json:"version"`

	// Revision is the build/optimization job id (a string, e.g. "76937").
	Revision string `json:"revision,omitempty"`
	Task     string `json:"task"`
	Family   string `json:"family,omitempty"`
	Backend  string `json:"backend"`
	// ServerFamily is the concrete model-server product serving the model
	// (e.g. ovms/triton/external), reported by newer edges; empty on older
	// firmware. Distinct from Backend, which is the wire protocol family.
	ServerFamily string `json:"server_family,omitempty"`
	ModelName    string `json:"model_name,omitempty"`

	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`

	// Labels are stored/displayed verbatim; the kernel never interprets or
	// internationalizes them.
	Labels []string `json:"labels"`

	ActiveBindings int `json:"active_bindings"`

	// Stale marks a row whose gw-manifest lease expired. It is kept (not
	// pruned) until the gateway reports the models segment again.
	Stale     bool       `json:"stale"`
	StaleAt   *time.Time `json:"stale_at,omitempty"`
	FirstSeen time.Time  `json:"first_seen"`
	UpdatedAt time.Time  `json:"updated_at"`

	// failedAlarm is the at-most-once claim flag for the FAILED alarm.
	failedAlarm bool
}

// Key returns the inventory key as "modelID\x00version".
func (m Model) Key() string { return m.ModelID + "\x00" + m.Version }

// Input is one reported model entry accepted by the reconciler. It is a
// transport-independent projection of manifest.ModelInstance mapped by the
// ingress, keeping this package free of an ingest-package dependency.
type Input struct {
	ID             string
	Version        string
	Revision       string
	Task           string
	Family         string
	Backend        string
	ServerFamily   string
	ModelName      string
	State          string
	Reason         string
	Labels         []string
	ActiveBindings int
}

// Validate reports whether the entry is safe to upsert. The inventory key
// and a lifecycle state are required; the rest is opaque business data.
func (in Input) Validate() error {
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.Version) == "" {
		return ErrInvalidModel
	}
	switch in.State {
	case StateExtracting, StateValidating, StateReady, StateFailed, StateRemoved:
	default:
		return ErrInvalidModel
	}
	return nil
}

// PrunedEntry records a model that disappeared from a present full list and
// was therefore hard-deleted.
type PrunedEntry struct {
	ModelID   string
	Version   string
	LastState string
}

// ReconcileResult summarizes one full-replacement reconciliation cycle. It
// drives alarm open/clear in the service without a second round trip.
type ReconcileResult struct {
	// Pruned are models deleted this cycle (an audit row was written per).
	Pruned []PrunedEntry
	// ClearedFailed are model keys whose FAILED alarm must clear: models
	// that left FAILED, plus pruned models that were FAILED.
	ClearedFailed []Model
	// WasStale is true when the gateway previously had stale rows, i.e.
	// the per-gateway inventory-stale alarm must clear.
	WasStale bool
	// Upserted is the post-reconcile inventory for the gateway.
	Upserted []Model
}

// Changed reports whether the cycle should emit a console event.
func (r ReconcileResult) Changed() bool {
	return len(r.Pruned) > 0 || r.WasStale || len(r.Upserted) > 0
}

// RuntimeSnapshot is the parsed device-shadow reported "inference" block.
type RuntimeSnapshot struct {
	State          string
	Endpoint       string
	Protocol       string
	ModelsReady    int
	ModelsTotal    int
	ActiveBindings int
	LatencyMS      float64
	CheckedAt      time.Time
	ProbeIntervalS float64
	// Present reports whether the shadow carried an inference block at all.
	Present bool
}

// classifyRuntime decides the effective runtime plane state at time now.
// A DEGRADED or ONLINE block that is fresh is healthy (no alarm); OFFLINE is
// a failure regardless of freshness; a block older than three probe
// intervals is silent and treated as UNKNOWN/stale. A missing/empty block
// returns "" so the caller can skip a gateway that has never reported.
func (s RuntimeSnapshot) classifyRuntime(now time.Time) string {
	if !s.Present {
		return ""
	}
	if s.State == RuntimeOffline {
		return RuntimeOffline
	}
	if s.ProbeIntervalS > 0 && !s.CheckedAt.IsZero() {
		limit := time.Duration(s.ProbeIntervalS*runtimeStaleLimit*1000) * time.Millisecond
		if now.Sub(s.CheckedAt) > limit {
			return RuntimeUnknown
		}
	}
	if s.State == RuntimeOnline || s.State == RuntimeDegraded {
		return s.State
	}
	return s.State
}

// DefaultInventoryStaleThreshold bounds how long a negotiated gateway may
// keep omitting the models segment while other manifest segments (devices/
// connectors) keep renewing the frame lease. It mirrors the "three missed
// cycles" watchdog convention against the manifest's ~5-minute full-report
// cadence (change-debounced frames also carry the segment, so a healthy edge
// refreshes the watermark far more often).
const DefaultInventoryStaleThreshold = 15 * time.Minute

// Stale reasons recorded on GATEWAY_MODEL_STALE details.
const (
	// StaleReasonLeaseExpired: no manifest frame at all (shared lease TTL).
	StaleReasonLeaseExpired = "manifest_lease_expired"
	// StaleReasonSegmentSilent: manifest frames keep arriving but no longer
	// carry the models segment on a negotiated gateway.
	StaleReasonSegmentSilent = "models_segment_stale"
)

// GatewayRef identifies one gateway scoped by tenant.
type GatewayRef struct {
	TenantID  string
	GatewayID string
}

// EventFrame is one user-plane model-inventory notification delivered over
// Core NATS to the console WebSocket hub. Like the connector frame it is a
// change notification: the console re-fetches the gateway's model list.
type EventFrame struct {
	Kind string `json:"kind"`

	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`
}

// Event kinds carried on SubjectEventPrefix.
const (
	// EventInventory signals a models reconciliation cycle changed rows.
	EventInventory = "inventory"
	// EventStale signals the inventory was marked stale (lease expiry) or
	// recovered from staleness.
	EventStale = "stale"
	// EventRuntime signals a runtime-plane alarm state changed.
	EventRuntime = "runtime"
)

// SubjectEventPrefix is the Core NATS root "nl.model.event.<tenant>".
const SubjectEventPrefix = "nl.model.event"

// EventWildcard matches every model-inventory event subject.
const EventWildcard = "nl.model.event.>"

// rawJSON returns a non-nil JSON document for a jsonb column.
func rawJSON(v any, fallback string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(fallback)
	}
	return b
}
