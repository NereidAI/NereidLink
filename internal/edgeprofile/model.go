// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package edgeprofile caches EdgeX device profiles reported by gateways on
// v1/gateway/profiles and turns them into native NereidLink device profiles on
// demand.
//
// The package is deliberately split from the connector machinery: a connector
// instance references an EdgeX profile by name, while this package owns the
// profile *content*. Caching is per (tenant, gateway, name): the same profile
// name reported by two gateways is two cached rows, because adoption is offered
// from the concrete reporting gateway. Adoption never happens implicitly; an
// operator explicitly adopts one cached profile into a native device profile.
package edgeprofile

import "time"

// Resource is the minimal EdgeX deviceResource subset NL needs to build a
// measurement descriptor. The JSON tags intentionally match the device SDK
// edge uplink shape so the wire payload caches verbatim.
type Resource struct {
	// Name is the resource name and equals the telemetry measurement key.
	Name string `json:"name"`

	// ValueType is the EdgeX value type (Bool, Int32, Float64, String, ...).
	ValueType string `json:"valueType"`

	// Units is the optional engineering unit.
	Units string `json:"units,omitempty"`

	// Description is the optional human description, carried but not yet
	// placed on a native descriptor.
	Description string `json:"description,omitempty"`
}

// Profile is one EdgeX device profile as reported by the edge.
type Profile struct {
	// Name is the EdgeX device profile name.
	Name string `json:"name"`

	// DeviceResources maps resource name/value type to measurement keys.
	DeviceResources []Resource `json:"deviceResources"`
}

// Frame is the v1/gateway/profiles uplink payload.
type Frame struct {
	// Profiles lists every profile in this report.
	Profiles []Profile `json:"profiles"`
}

// CachedProfile is one cached edge profile row with its adoption linkage.
type CachedProfile struct {
	// TenantID owns the row.
	TenantID string

	// GatewayID is the reporting gateway device.
	GatewayID string

	// Name is the EdgeX profile name.
	Name string

	// ContentHash is the platform-computed canonical sha256.
	ContentHash string

	// ResourceCount is len(DeviceResources) in the raw report.
	ResourceCount int

	// FirstSeenAt is when the gateway first reported this name.
	FirstSeenAt time.Time

	// LastSeenAt is the most recent report time.
	LastSeenAt time.Time

	// AdoptedProfileID is the native profile created/updated by adopt.
	AdoptedProfileID string

	// AdoptedAt is when the linkage was last set.
	AdoptedAt *time.Time
}

// AdoptMode controls adopt conflict handling.
type AdoptMode string

const (
	// AdoptDefault creates the native profile when no same-name profile
	// exists, no-ops when this cached row already owns that profile, and
	// returns a conflict otherwise.
	AdoptDefault AdoptMode = ""

	// AdoptUpdate overwrites the descriptors of the resolved target native
	// profile and links this cached row to it.
	AdoptUpdate AdoptMode = "update"
)

// AdoptRequest selects one cached profile and the adoption strategy.
type AdoptRequest struct {
	// GatewayID is the reporting gateway the cached row belongs to.
	GatewayID string

	// Name is the cached edge profile name.
	Name string

	// Mode is "" (default) or "update".
	Mode AdoptMode

	// TargetName optionally adopts under a different native profile name.
	// Empty means use the edge profile name.
	TargetName string
}

// SkippedResource records a deviceResource that could not be translated into a
// native descriptor, while the raw profile stays fully cached.
type SkippedResource struct {
	// Name is the resource name.
	Name string

	// ValueType is the unsupported EdgeX value type.
	ValueType string

	// Reason explains why it was skipped.
	Reason string
}

// Detail is one cached edge profile with its raw resources and the adopt
// classification of each. It backs the read-only profile content view.
type Detail struct {
	// Cached carries the cache metadata (name, hash, counts, adoption link).
	Cached CachedProfile

	// Resources lists every reported deviceResource in report order with the
	// decision Translate would make at adopt time.
	Resources []ResourceClass
}

// AdoptResult reports the outcome of one adopt action.
type AdoptResult struct {
	// ProfileID is the resolved native device profile id.
	ProfileID string

	// ProfileName is the native profile name.
	ProfileName string

	// Created reports a new native profile was created.
	Created bool

	// Updated reports an existing native profile's descriptors were replaced.
	Updated bool

	// AdoptedCount is the number of descriptors translated from resources.
	AdoptedCount int

	// Skipped lists resources excluded from translation.
	Skipped []SkippedResource
}
