// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package command implements command routing defined in ADR-0012: presence
// lookup, realtime/persistent timeliness, one-way/request-response
// interaction, timeouts, persistent delivery after reconnect and expiry.
//
// The authoritative pending queue is PostgreSQL; Core NATS only carries the
// dispatch request to the owning ingress node, so router or node crashes do
// not lose queued commands.
package command

import (
	"errors"
	"time"

	pubcontract "github.com/nereidai/nereidlink/sdk/contract"
)

// DeviceStateActive is the only lifecycle state that accepts commands.
const DeviceStateActive = "ACTIVE"

// ErrDeviceInactive indicates a command targeted a device that is not
// ACTIVE (unclaimed, decommissioned or archived).
var ErrDeviceInactive = errors.New("device is not active")

// Interaction modes declared by the Command Catalog.
const (
	// InteractionOneWay completes as soon as the frame is delivered.
	InteractionOneWay = pubcontract.InteractionOneWay

	// InteractionRequestResponse waits for one device response.
	InteractionRequestResponse = pubcontract.InteractionRequestResponse
)

// Timeliness modes declared by the Command Catalog.
const (
	// TimelinessRealtime fails immediately when the device is offline.
	TimelinessRealtime = "realtime"

	// TimelinessPersistent queues while offline for delivery on reconnect.
	TimelinessPersistent = "persistent"
)

// Direction names the south-side operation a command performs. It tells the
// edge whether to read (return a value, no side effects) or to write (apply
// the params document).
const (
	// DirectionRead performs the edge's read operation (e.g. EdgeX GET).
	DirectionRead = "read"

	// DirectionWrite performs the edge's write operation (e.g. EdgeX PUT).
	DirectionWrite = "write"
)

// Catalog provenance values.
const (
	// SourceManual marks an operator-declared catalog entry; automatic
	// materialization must never modify or remove it.
	SourceManual = "manual"

	// SourceAuto marks an entry materialized from the bound device profile's
	// readable/writable measurement points.
	SourceAuto = "auto"
)

// Command lifecycle statuses are re-exported from the public SDK
// contract so adapters (possibly external repositories) and the core
// share one authoritative definition of the wire values.
const (
	// StatusPending awaits delivery (persistent commands only).
	StatusPending = pubcontract.StatusPending

	// StatusClaimed is reserved by a router instance for delivery.
	StatusClaimed = pubcontract.StatusClaimed

	// StatusQueued is the synchronous result for an accepted persistent
	// command created while the device was offline.
	StatusQueued = pubcontract.StatusQueued

	// StatusDelivered means the frame reached the live transport session.
	StatusDelivered = pubcontract.StatusDelivered

	// StatusSucceeded carries a successful device response.
	StatusSucceeded = pubcontract.StatusSucceeded

	// StatusFailed carries a device or transport failure.
	StatusFailed = pubcontract.StatusFailed

	// StatusTimeout means no response arrived before the deadline.
	StatusTimeout = pubcontract.StatusTimeout

	// StatusNotConnected means a realtime command met an offline device.
	StatusNotConnected = pubcontract.StatusNotConnected

	// StatusModuleOffline means the add-on module owning the device
	// protocol is not enrolled, disabled or has a stale heartbeat.
	StatusModuleOffline = pubcontract.StatusModuleOffline

	// StatusExpired means a persistent command exceeded its TTL undelivered.
	StatusExpired = pubcontract.StatusExpired
)

// Core NATS subject templates.
const (
	// DispatchSubjectPrefix carries dispatch requests per ingress node.
	DispatchSubjectPrefix = pubcontract.DispatchSubjectPrefix

	// EventsSubjectPrefix carries lifecycle events such as expiry or a
	// late response after the synchronous wait already ended.
	EventsSubjectPrefix = pubcontract.EventsSubjectPrefix

	// eventsWildcard observes lifecycle events for every tenant and device.
	eventsWildcard = EventsSubjectPrefix + "*.*"

	// presenceWildcard observes all presence boundaries.
	presenceWildcard = "nl.presence.>"
)

// Command is a validated command ready for routing.
type Command struct {
	// ID is the globally unique correlation identifier.
	ID string

	// TenantID owns the target device.
	TenantID string

	// DeviceID is the target device.
	DeviceID string

	// Name identifies the catalog entry.
	Name string

	// Params is the raw JSON parameter document.
	Params []byte

	// Interaction is one-way or request-response.
	Interaction string

	// Timeliness is realtime or persistent.
	Timeliness string

	// Timeout bounds the request-response wait.
	Timeout time.Duration

	// Direction is read or write; empty means the edge infers it from its
	// own capability metadata.
	Direction string

	// CreatedAt is the platform creation time.
	CreatedAt time.Time
}

// Ack is the result of a dispatch or a lifecycle completion.
type Ack struct {
	// ID correlates the ack with the command.
	ID string

	// Status is the terminal or synchronous lifecycle status.
	Status string

	// Response carries the raw JSON device response.
	Response []byte

	// Error carries a machine-readable failure detail.
	Error string

	// FinishedAt is the transition time.
	FinishedAt time.Time
}

// Event is the JSON payload published to a command events subject.
type Event = pubcontract.Event

// dispatchSubject builds the ingress-specific request subject.
func dispatchSubject(ingressID string) string {
	return DispatchSubjectPrefix + ingressID
}

// eventsSubject builds the per-device lifecycle event subject.
func eventsSubject(tenantID, deviceID string) string {
	return EventsSubjectPrefix + tenantID + "." + deviceID
}
