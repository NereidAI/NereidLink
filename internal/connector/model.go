// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package connector implements the ADR-0026 connector-instance desired/
// reported lifecycle. NL is the system of record for the desired spec
// (`{connector_type, type_version, config, mappings, revision}`); the edge
// applies it and reports APPLIED/FAILED/DEGRADED plus the revision it
// applied. Specs are pushed to a connected gateway over MQTT and re-sent
// whenever the gateway (re)completes hello, so desired never disappears and
// a reconnect retries delivery without per-gateway timers in the kernel.
//
// Config and mappings are opaque JSON: the NL kernel does not understand
// any connector protocol, and adding a connector type must not require an
// NL upgrade. Edge-registered JSON-schema validation is a later slice.
package connector

import (
	"encoding/json"
	"time"
)

// Alarm types for the two lifecycle escalations. A spec that never reaches
// APPLIED raises the pending alarm (WARNING at 5 minutes); a spec the edge
// explicitly reports FAILED raises the failed alarm (MAJOR immediately,
// CRITICAL once the failure persists for one hour).
const (
	AlarmTypePending = "GATEWAY_CONNECTOR_PENDING"
	AlarmTypeFailed  = "GATEWAY_CONNECTOR_FAILED"
	// AlarmTypeDegraded is the per-spec WARNING raised while the edge
	// reports DEGRADED (ADR-0039 binding plane: e.g. reason
	// idle-no-inference). It is generic across every connector type, so the
	// edge-inference connector needs no kernel special-casing.
	AlarmTypeDegraded = "GATEWAY_CONNECTOR_DEGRADED"
	// AlarmTypeDrift is the per-gateway reconciliation alarm raised when the
	// edge's applied connector set diverges from desired (missing/stale/
	// drifted instances). One alarm per gateway, cleared when back in sync.
	AlarmTypeDrift = "GATEWAY_CONNECTOR_DRIFT"
)

// Dedupe keys are per spec so one stuck/failed spec keeps a single alarm and
// an eventual APPLIED or new revision clears exactly that alarm. The drift
// alarm is one per gateway because it summarizes the whole applied set.
const (
	dedupePendingPrefix  = "gateway-connector-pending:"
	dedupeFailedPrefix   = "gateway-connector-failed:"
	dedupeDegradedPrefix = "gateway-connector-degraded:"
	dedupeDriftPrefix    = "gateway-connector-drift:"
)

// DedupePendingKey returns the not-applied alarm dedupe key for a spec.
func DedupePendingKey(specID string) string { return dedupePendingPrefix + specID }

// DedupeFailedKey returns the FAILED alarm dedupe key for a spec.
func DedupeFailedKey(specID string) string { return dedupeFailedPrefix + specID }

// DedupeDegradedKey returns the DEGRADED alarm dedupe key for a spec.
func DedupeDegradedKey(specID string) string { return dedupeDegradedPrefix + specID }

// DedupeDriftKey returns the manifest-drift alarm dedupe key for a gateway.
func DedupeDriftKey(gatewayID string) string { return dedupeDriftPrefix + gatewayID }

// Lifecycle states reported by the edge (and PENDING before first report).
const (
	StatePending    = "PENDING"
	StateDispatched = "DISPATCHED"
	StateCreating   = "CREATING"
	StateApplied    = "APPLIED"
	StateFailed     = "FAILED"
	StateDegraded   = "DEGRADED"
)

// openStates are the states that mean "the edge has not confirmed APPLIED".
var openStates = map[string]struct{}{
	StatePending: {}, StateDispatched: {}, StateCreating: {},
}

// IsOpen reports whether state still awaits an APPLIED/FAILED terminal-ish
// report.
func IsOpen(state string) bool {
	_, ok := openStates[state]
	return ok
}

// StatusStats contains independently optional NE job measurements. A non-nil
// pointer to zero is a real reading; nil means unavailable.
type StatusStats struct {
	SuccessTotal *uint64  `json:"success_total,omitempty"`
	ErrorTotal   *uint64  `json:"error_total,omitempty"`
	P50MS        *float64 `json:"p50_ms,omitempty"`
	P95MS        *float64 `json:"p95_ms,omitempty"`
}

// JobMetrics is the business-monitor projection of optional connector stats.
// EventsPerSecond remains nil until the wire protocol supplies a real source.
type JobMetrics struct {
	SuccessTotal    *uint64
	ErrorTotal      *uint64
	P50MS           *float64
	P95MS           *float64
	EventsPerSecond *float64
}

// DeleteTombstone is durable explicit-delete intent. DISPATCHED records only a
// transport attempt; it is not an edge acknowledgement and never implies GC.
type DeleteTombstone struct {
	TenantID       string
	GatewayID      string
	SpecID         string
	DeviceID       string
	ConnectorType  string
	TypeVersion    string
	DeleteRevision int64
	State          string
	AttemptCount   int64
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastAttemptAt  *time.Time
}

// Frame reconstructs the exact idempotent downlink delete.
func (t DeleteTombstone) Frame() PushFrame {
	return PushFrame{
		Kind: KindDelete, TenantID: t.TenantID, GatewayID: t.GatewayID,
		SpecID: t.SpecID, DeviceID: t.DeviceID, Revision: t.DeleteRevision,
	}
}

// Spec is one desired connector instance plus its last reported state.
type Spec struct {
	// ID is the spec_id addressed by the edge.
	ID string `json:"spec_id"`

	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`

	// Desired half.
	ConnectorType string          `json:"connector_type"`
	TypeVersion   string          `json:"type_version"`
	Config        json.RawMessage `json:"config"`
	Mappings      json.RawMessage `json:"mappings"`
	Revision      int64           `json:"revision"`

	// HealthCheck is the instance-level liveness probe policy the edge
	// runs against the bound sub-device. Empty means probing disabled.
	HealthCheck HealthCheck `json:"health_check"`

	// DeviceID optionally binds this instance to one south-bound sub-device
	// of the gateway (ADR-0026 two-level config). Empty means a gateway-level
	// instance. At most one instance may bind a given sub-device.
	DeviceID string `json:"device_id,omitempty"`

	// ProfileID optionally references the device profile supplying the
	// connector template (type/version + default config/mappings). Empty
	// means the instance carries its full config/mappings itself.
	ProfileID string `json:"profile_id,omitempty"`

	// Reported half.
	State            string     `json:"state"`
	ReportedRevision int64      `json:"reported_revision"`
	ReportReason     string     `json:"report_reason,omitempty"`
	NotAppliedAlarm  bool       `json:"not_applied_alarm"`
	FailedAlarm      bool       `json:"failed_alarm"`
	FailedCritical   bool       `json:"failed_critical"`
	FailedAt         *time.Time `json:"failed_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DesiredAt        time.Time  `json:"desired_at"`
	DispatchedAt     *time.Time `json:"dispatched_at,omitempty"`
	ReportedAt       *time.Time `json:"reported_at,omitempty"`

	// Optional NE job stats and manager GET readback. Every new accepted status
	// replaces this projection, so omitted fields cannot retain stale values.
	Stats            *StatusStats `json:"stats,omitempty"`
	ObservedRevision *int64       `json:"observed_revision,omitempty"`
	ObservedHash     string       `json:"observed_hash,omitempty"`

	// Manifest watermark (migration 0044). ManifestRevision/ManifestHash are
	// the applied revision and optional effective-config hash the gateway
	// last reported in its full-list manifest; LastManifestAt is when that
	// full list was seen. A desired spec absent from the manifest keeps its
	// previous watermark (NULL until first seen), which is how "missing" is
	// detected.
	ManifestRevision int64      `json:"manifest_revision"`
	ManifestHash     string     `json:"reported_config_hash,omitempty"`
	LastManifestAt   *time.Time `json:"last_manifest_at,omitempty"`
}

// SaveInput carries the desired fields written from REST. The revision is
// assigned by the store on create and incremented on each desired update.
//
// In the two-level model, ProfileID may reference a device profile carrying
// a connector template; Config/Mappings then hold per-device OVERRIDES that
// win over the template at dispatch. ConnectorType/TypeVersion may be left
// empty when a profile is supplied, in which case they resolve from that
// profile's template.
type SaveInput struct {
	ConnectorType string          `json:"connector_type"`
	TypeVersion   string          `json:"type_version"`
	Config        json.RawMessage `json:"config"`
	Mappings      json.RawMessage `json:"mappings"`
	DeviceID      string          `json:"device_id,omitempty"`
	ProfileID     string          `json:"profile_id,omitempty"`
	// HealthCheck is the per-instance liveness probe policy. It is never
	// inherited from the profile template: probing is an operational,
	// per-device decision. Omit or send {} to disable.
	HealthCheck json.RawMessage `json:"health_check,omitempty"`
}

// Template is the connector half of a device profile. It supplies the
// default type/version, config and point mappings merged under an instance's
// per-device overrides. Satisfied by a small projection read from the
// registry at merge time.
type Template struct {
	ProfileID     string
	ConnectorType string
	TypeVersion   string
	Config        json.RawMessage
	Mappings      json.RawMessage
	// Protocol is the profile library protocol family (ADR-0032); "" unknown.
	Protocol string
}

// StatusFrame is the uplink on `v1/gateway/connectors/status`.
type StatusFrame struct {
	// SpecID identifies the desired spec the edge applied.
	SpecID string `json:"spec_id"`

	// State is one of CREATING/APPLIED/FAILED/DEGRADED.
	State string `json:"state"`

	// Revision is the desired revision the edge applied.
	Revision int64 `json:"revision"`

	// Reason explains FAILED/DEGRADED.
	Reason string `json:"reason,omitempty"`

	// Stats carries optional per-field job measurements. Nil/empty is
	// unavailable; an explicit zero counter remains meaningful.
	Stats *StatusStats `json:"stats,omitempty"`

	// ObservedRevision/ObservedHash describe authoritative manager GET
	// readback. They are distinct from desired/manifest revision/config hash.
	ObservedRevision *int64 `json:"observed_revision,omitempty"`
	ObservedHash     string `json:"observed_hash,omitempty"`
}

// Frame kinds delivered on the downlink.
const (
	// KindSpec delivers or updates one desired spec.
	KindSpec = "spec"
	// KindDelete removes one connector instance on the edge.
	KindDelete = "delete"
)

// PushFrame is the Core NATS downlink message consumed by the MQTT ingress
// relay and written to the gateway's spec/delete topic.
type PushFrame struct {
	// Kind is KindSpec or KindDelete.
	Kind string `json:"kind"`

	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`

	// SpecID is always present; delete also carries revision and optional device_id.
	SpecID string `json:"spec_id"`

	// DeviceID identifies the south-bound sub-device a bound instance
	// targets; empty for a gateway-level instance. Present on both spec and
	// delete frames so the edge keys removal the same way.
	DeviceID string `json:"device_id,omitempty"`

	// Desired fields, present for KindSpec.
	ConnectorType string          `json:"connector_type,omitempty"`
	TypeVersion   string          `json:"type_version,omitempty"`
	Config        json.RawMessage `json:"config,omitempty"`
	Mappings      json.RawMessage `json:"mappings,omitempty"`
	// HealthCheck is the instance probe policy; omitted when disabled.
	HealthCheck *HealthCheck `json:"health_check,omitempty"`
	Revision    int64        `json:"revision"`
}

// SubjectDesiredPrefix is the Core NATS root for desired pushes, suffixed
// "<tenant>.<gateway>".
const SubjectDesiredPrefix = "nl.connector.desired"

// SubjectEventPrefix is the Core NATS root for user-plane connector
// lifecycle events consumed by the console WebSocket hub. Layout is
// "nl.connector.event.<tenant>" (tenant on token 4), intentionally separate
// from the downlink desired prefix the MQTT relay subscribes to.
const SubjectEventPrefix = "nl.connector.event"

// EventWildcard matches every connector lifecycle event subject.
const EventWildcard = "nl.connector.event.>"

// Connector lifecycle event kinds carried on SubjectEventPrefix. They are
// change notifications only: the console re-fetches the gateway's connector
// list and reconcile view rather than mutating state from the frame.
const (
	// EventDesired signals a desired spec create/update was pushed.
	EventDesired = "desired"

	// EventDeleted signals a desired spec was removed.
	EventDeleted = "deleted"

	// EventStatus signals an edge-reported status frame was applied.
	EventStatus = "status"

	// EventManifest signals a reported-manifest reconciliation cycle ran.
	EventManifest = "manifest"
)

// EventFrame is one user-plane connector lifecycle notification.
type EventFrame struct {
	// Kind is one of the Event* constants.
	Kind string `json:"kind"`

	// TenantID scopes the frame; it is also encoded in the NATS subject and
	// must never be trusted from the payload for fanout boundaries.
	TenantID string `json:"tenant_id"`

	// GatewayID identifies the gateway whose connector set changed.
	GatewayID string `json:"gateway_id"`

	// SpecID identifies the affected instance; empty for a whole-manifest event.
	SpecID string `json:"spec_id,omitempty"`

	// State is the edge-reported lifecycle state for EventStatus.
	State string `json:"state,omitempty"`

	// Revision is the desired revision relevant to the event.
	Revision int64 `json:"revision,omitempty"`
}

// ReportedInstance is one entry of the connector segment in the gateway's
// full-state manifest uplink. The manifest lists every connector instance
// the edge currently has applied; an instance desired by NL but absent from
// this list is "missing" for the cycle.
type ReportedInstance struct {
	// SpecID is the desired spec id the edge applied.
	SpecID string `json:"spec_id"`

	// Revision is the desired revision the edge applied. An older revision
	// than NL's current desired is "stale".
	Revision int64 `json:"revision"`

	// ConfigHash is the optional sha256 (lowercase hex) of the edge's
	// canonical effective config+mappings document. Empty means the edge
	// does not report a hash, in which case content drift at the same
	// revision cannot be detected (revision/presence reconciliation only).
	ConfigHash string `json:"config_hash,omitempty"`
}

// Reconcile class name constants for a desired spec versus the manifest.
const (
	// ClassInSync: desired and reported at the same revision (and, when
	// hashes are reported, identical effective content).
	ClassInSync = "in_sync"
	// ClassMissing: desired by NL but absent from the edge's full list.
	ClassMissing = "missing"
	// ClassStale: reported but at an older desired revision.
	ClassStale = "stale"
	// ClassDrifted: reported at the same revision with a different config
	// hash (edge-local mutation).
	ClassDrifted = "drifted"
)

// ReconcileReport summarizes one manifest reconciliation for a gateway. The
// four desired-side classes are partitioned by spec id; Extra holds spec ids
// the edge reported that are not desired for this gateway.
type ReconcileReport struct {
	TenantID  string   `json:"tenant_id"`
	GatewayID string   `json:"gateway_id"`
	InSync    []string `json:"in_sync"`
	Missing   []string `json:"missing"`
	Stale     []string `json:"stale"`
	Drifted   []string `json:"drifted"`
	// Extra are reported spec ids not present in NL's desired set. In strict
	// mode NL sends delete frames for these; it never deletes a desired row
	// for them because they are unknown to NL.
	Extra []string `json:"extra"`
}

// Drift reports whether the applied set differs from desired.
func (r ReconcileReport) Drift() bool {
	return len(r.Missing) > 0 || len(r.Stale) > 0 ||
		len(r.Drifted) > 0 || len(r.Extra) > 0
}

// ManifestWatermark is the last full connector list a gateway reported. It
// lets a read-only console query classify desired-vs-applied without
// accepting a new manifest: a desired spec absent from SpecIDs is missing,
// and a reported id with no desired row is extra (migration 0055).
type ManifestWatermark struct {
	TenantID   string    `json:"tenant_id"`
	GatewayID  string    `json:"gateway_id"`
	ObservedAt time.Time `json:"observed_at"`
	// SpecIDs is every spec id the edge listed in its most recent manifest.
	SpecIDs []string `json:"spec_ids"`
}

// ReconcileView is the read-only projection of one gateway's connector set:
// the last manifest watermark plus the current desired-side classification.
// Unlike ReconcileReport it is computed on demand, raises no alarm and never
// mutates state.
type ReconcileView struct {
	TenantID   string             `json:"tenant_id"`
	GatewayID  string             `json:"gateway_id"`
	Watermark  *ManifestWatermark `json:"watermark,omitempty"`
	InSync     []string           `json:"in_sync"`
	Missing    []string           `json:"missing"`
	Stale      []string           `json:"stale"`
	Drifted    []string           `json:"drifted"`
	Extra      []string           `json:"extra"`
	StrictMode bool               `json:"strict_mode"`
}
