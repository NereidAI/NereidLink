// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package discovery holds the transport-independent gateway auto-provisioning
// logic (ADR-0026 / ADR-0013). A NereidEdge gateway publishes the local
// devices it can see on a southbound bus; the platform materialises any that
// are not yet registered, bound to that gateway, and returns the mapping from
// each gateway-local id to the platform device UUID. The edge then uses the
// UUID on the normal manifest/connect topics, so admission keeps its single
// UUID-based code path.
//
// This package deliberately contains no MQTT types. It converts an uplink
// frame into registry provisioning calls and builds a result frame; the MQTT
// ingress owns framing, ACL and the downlink publish.
package discovery

// Status values returned per candidate.
const (
	// StatusCreated means a new device row was inserted.
	StatusCreated = "created"
	// StatusExisting means the deterministic name already matched a device
	// bound to this gateway; its UUID was returned without a new row.
	StatusExisting = "existing"
	// StatusRejected means the candidate could not be provisioned; Reason
	// explains why and the rest of the batch is unaffected.
	StatusRejected = "rejected"
)

// Candidate is one gateway-local device reported in a discovery frame.
type Candidate struct {
	// LocalID is the stable key inside the gateway's southbound domain. It is
	// required and drives the deterministic canonical name.
	LocalID string `json:"local_id"`

	// Name optionally overrides the generated "<gateway>/<local>" canonical
	// name. Most edges leave it empty.
	Name string `json:"name,omitempty"`

	// Profile optionally references a tenant profile by UUID or name. When
	// empty the policy default profile is used.
	Profile string `json:"profile,omitempty"`
}

// Frame is the uplink payload on v1/gateway/discovery. The tenant and gateway
// are NOT taken from the frame; the ingress derives them from the
// authenticated, negotiated gateway connection.
type Frame struct {
	// Type optionally identifies the discovery source/bus for audit and
	// future routing. It is opaque to the platform.
	Type string `json:"type,omitempty"`

	// Candidates is the set of local devices seen by the gateway.
	Candidates []Candidate `json:"candidates"`
}

// ItemResult reports the outcome for one candidate, keyed by LocalID so the
// edge can correlate without echoing the whole request.
type ItemResult struct {
	LocalID       string `json:"local_id"`
	DeviceID      string `json:"device_id,omitempty"`
	CanonicalName string `json:"canonical_name,omitempty"`
	ProfileID     string `json:"profile_id,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
}

// Result is the downlink payload on v1/gateway/discovery/result. It is always
// emitted as an array-bearing object, never null, per the API contract.
type Result struct {
	Results []ItemResult `json:"results"`
}
