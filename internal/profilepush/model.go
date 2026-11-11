// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package profilepush delivers tenant device-profile library content to
// gateways that need it (ADR-0038).
//
// The tenant device_profile library (ADR-0032) is the single content source of
// record: a profile reported by gateway A is a tenant asset that gateway B can
// reuse without re-authoring it. Delivery only ever references a library entry
// and its revision; the document is *rendered* per the format each gateway
// declares, so the kernel stays protocol-free and EdgeX is just one adapter.
//
// Semantics mirror the connector and edge-secret delivery already in place:
// NL is the system of record for the desired state, the full set is pushed
// idempotently and re-sent after every hello, the edge de-duplicates by
// (name, revision) and reports APPLIED/FAILED/REJECTED, and a stuck or failed
// delivery raises the same pending/failed alarms.
package profilepush

import (
	"errors"
	"time"
)

// Delivery states. PENDING/DISPATCHED/REMOVING are platform-side only and may
// never be reported by the edge.
const (
	StatePending    = "PENDING"
	StateDispatched = "DISPATCHED"
	StateApplied    = "APPLIED"
	StateFailed     = "FAILED"
	StateRejected   = "REJECTED"
	StateRemoving   = "REMOVING"
	StateRemoved    = "REMOVED"
)

// Render format identifiers. The neutral format is always available and is the
// contract a third-party edge implements; a format is only ever sent when the
// gateway declared it in hello.
const (
	// FormatNeutral is the vendor-neutral profile document (ADR-0032 §3). It
	// is byte-compatible with the uplink shape, so an edge that reports
	// profiles already understands it.
	FormatNeutral = "nereid-neutral/1"
	// FormatEdgeX is the EdgeX DeviceProfile shape (deviceResources /
	// deviceCommands). It is a cloud-side adapter, not a kernel dependency.
	FormatEdgeX = "edgex-device-profile/3"
)

// Alarm types raised by the delivery sweep (mirror connector/secret alarms).
const (
	// AlarmTypePending is the WARNING not-applied alarm for a stuck profile.
	AlarmTypePending = "GATEWAY_PROFILE_PENDING"
	// AlarmTypeFailed is the MAJOR/CRITICAL delivery-failure alarm.
	AlarmTypeFailed = "GATEWAY_PROFILE_FAILED"
)

const (
	dedupePendingPrefix = "gateway-profile-pending:"
	dedupeFailedPrefix  = "gateway-profile-failed:"
)

// DedupePendingKey returns the not-applied alarm dedupe key for a profile.
func DedupePendingKey(profileID string) string { return dedupePendingPrefix + profileID }

// DedupeFailedKey returns the FAILED alarm dedupe key for a profile.
func DedupeFailedKey(profileID string) string { return dedupeFailedPrefix + profileID }

// SubjectDesiredPrefix is the Core NATS root for per-gateway profile delivery
// frames: "nl.edge.profile.desired.<tenant>.<gateway>".
const SubjectDesiredPrefix = "nl.edge.profile.desired"

// SubjectEventPrefix is the Core NATS root for user-plane delivery
// notifications: "nl.profile.event.<tenant>".
const SubjectEventPrefix = "nl.profile.event"

// EventWildcard matches every delivery event subject.
const EventWildcard = "nl.profile.event.>"

// Delivery event kinds. They are change notifications only: the console
// re-fetches the gateway's profile list rather than mutating from the frame.
const (
	// EventDesired signals a desired profile create/update was pushed.
	EventDesired = "desired"
	// EventWithdrawn signals a profile target removal.
	EventWithdrawn = "withdrawn"
	// EventStatus signals an edge-reported status was applied.
	EventStatus = "status"
)

// Errors surfaced to REST callers.
var (
	// ErrNotFound reports a missing profile or delivery row.
	ErrNotFound = errors.New("profile delivery not found")
	// ErrInvalid reports a structurally invalid request.
	ErrInvalid = errors.New("invalid profile delivery request")
	// ErrUnknownStatus reports an edge status value the platform does not
	// accept as an uplink.
	ErrUnknownStatus = errors.New("unknown profile delivery status")
)

// EventFrame is one user-plane profile delivery notification.
type EventFrame struct {
	// Kind is one of the Event* constants.
	Kind string `json:"kind"`
	// TenantID scopes the frame; it is also encoded in the subject.
	TenantID string `json:"tenant_id"`
	// GatewayID identifies the gateway whose profile set changed.
	GatewayID string `json:"gateway_id"`
	// ProfileID identifies the affected profile; empty for a full-set event.
	ProfileID string `json:"profile_id,omitempty"`
	// ProfileName is the human-readable name for the notification.
	ProfileName string `json:"profile_name,omitempty"`
	// Status is the edge-reported status for EventStatus frames.
	Status string `json:"status,omitempty"`
	// Revision is the affected content revision.
	Revision int64 `json:"revision,omitempty"`
}

// Entry is one profile in a delivery frame.
type Entry struct {
	// ProfileID is the library profile id. It is included so the edge and
	// the dispatch marker can key by id rather than a renamed name.
	ProfileID string `json:"profile_id"`
	// Name is the tenant-unique library profile name.
	Name string `json:"name"`
	// Revision is the monotonic content revision.
	Revision int64 `json:"revision"`
	// Format is the render format identifier the document conforms to.
	Format string `json:"format"`
	// Removing marks a withdrawal; Document is absent when true.
	Removing bool `json:"removing,omitempty"`
	// Target is an optional opaque delivery hint for the edge (e.g. the
	// device service that should own the profile). The kernel never
	// interprets it.
	Target map[string]any `json:"target,omitempty"`
	// Document is the rendered profile document in the declared format.
	Document map[string]any `json:"document,omitempty"`
}

// PushFrame is the full desired profile set for one gateway.
type PushFrame struct {
	TenantID  string  `json:"tenant_id"`
	GatewayID string  `json:"gateway_id"`
	Entries   []Entry `json:"entries"`
}

// StatusFrame is the edge uplink for one gateway.
type StatusFrame struct {
	Reports []StatusEntry `json:"reports"`
}

// StatusEntry is one profile status report.
type StatusEntry struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Target is one explicit (profile, gateway) distribution row.
type Target struct {
	ProfileID   string    `json:"profile_id"`
	GatewayID   string    `json:"gateway_id"`
	GatewayName string    `json:"gateway_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Delivery is one (profile, gateway) delivery state row.
type Delivery struct {
	TenantID           string `json:"tenant_id"`
	ProfileID          string `json:"profile_id"`
	ProfileName        string `json:"profile_name"`
	GatewayID          string `json:"gateway_id"`
	DesiredRevision    int64  `json:"desired_revision"`
	DispatchedRevision int64  `json:"dispatched_revision"`
	AppliedRevision    int64  `json:"applied_revision"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	// Explicit reports whether an operator created a profile_gateway_target
	// edge. False means the gateway holds the profile only implicitly through
	// a connector instance; such a row is not withdrawable from the downlink
	// surface because nothing explicit exists to remove.
	Explicit     bool       `json:"explicit"`
	DesiredAt    time.Time  `json:"desired_at"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
	FailedAt     *time.Time `json:"failed_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// knownStatus reports whether an edge status value is an accepted uplink.
func knownStatus(status string) bool {
	switch status {
	case StateApplied, StateFailed, StateRejected, StateRemoved:
		return true
	default:
		return false
	}
}
