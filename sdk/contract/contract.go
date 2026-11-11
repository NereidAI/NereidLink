// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package contract defines the public wire contract between the
// NereidLink core and southbound adapters, including adapters developed
// in separate repositories.
//
// The package is limited to stable identifiers: Core NATS subject
// prefixes, command lifecycle statuses, interaction modes and the
// lifecycle event shape. It intentionally has no dependency on the
// platform internals, so an external adapter only depends on this
// package and the generated protocol buffers.
package contract

// DispatchSubjectPrefix carries command dispatch requests per ingress
// node; the full subject appends the ingress ID.
const DispatchSubjectPrefix = "nl.command.dispatch."

// EventsSubjectPrefix carries command lifecycle events (expiry, late
// response); the full subject appends "<tenant>.<device>".
const EventsSubjectPrefix = "nl.command.events."

// Interaction modes declared by the Command Catalog.
const (
	// InteractionOneWay completes as soon as the frame is delivered.
	InteractionOneWay = "one-way"

	// InteractionRequestResponse waits for one device response.
	InteractionRequestResponse = "request-response"
)

// Command lifecycle statuses. They appear on the wire in acks, events
// and the command state column, so they are a versioned contract rather
// than tunable values.
const (
	// StatusPending awaits delivery (persistent commands only).
	StatusPending = "PENDING"

	// StatusClaimed is reserved by a router instance for delivery.
	StatusClaimed = "CLAIMED"

	// StatusQueued answers a persistent command created while offline.
	StatusQueued = "QUEUED"

	// StatusDelivered means the frame reached the live transport session.
	StatusDelivered = "DELIVERED"

	// StatusSucceeded carries a successful device response.
	StatusSucceeded = "SUCCEEDED"

	// StatusFailed carries a device or transport failure.
	StatusFailed = "FAILED"

	// StatusTimeout means no response arrived before the deadline.
	StatusTimeout = "TIMEOUT"

	// StatusNotConnected means a realtime command met an offline device.
	StatusNotConnected = "NOT_CONNECTED"

	// StatusModuleOffline means the add-on module owning the protocol is
	// not enrolled, disabled or has a stale heartbeat.
	StatusModuleOffline = "MODULE_OFFLINE"

	// StatusExpired means a persistent command exceeded its TTL undelivered.
	StatusExpired = "EXPIRED"
)

// Event is the JSON payload published to a command events subject.
type Event struct {
	// CommandID identifies the command.
	CommandID string `json:"command_id"`

	// TenantID owns the device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the target device.
	DeviceID string `json:"device_id"`

	// Status is the lifecycle status.
	Status string `json:"status"`

	// Response carries the raw JSON device response when present.
	Response string `json:"response,omitempty"`

	// Time is the event time as RFC3339 UTC.
	Time string `json:"time"`
}
