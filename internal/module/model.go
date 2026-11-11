// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package module manages the registry of optional southbound protocol
// modules (CoAP, LwM2M, SNMP). Modules are platform-level add-ons running
// in separate containers: the core provisions a module row and an
// enrollment token, the adapter process registers with that token and
// renews a heartbeat lease, and the core flips stale leases to OFFLINE so
// commands for the module's devices fail fast instead of timing out.
package module

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound means no module matches the lookup.
var ErrNotFound = errors.New("module not found")

// ErrInvalidEnrollment means the adapter presented an unknown or stale
// enrollment token.
var ErrInvalidEnrollment = errors.New("invalid enrollment token")

// ErrTypeMismatch means the registering adapter type does not match the
// provisioned module type.
var ErrTypeMismatch = errors.New("module type mismatch")

// ErrInvalidArgument means the request failed validation.
var ErrInvalidArgument = errors.New("MODULE_INVALID_ARGUMENT")

// Supported types.
const (
	TypeCoAP  = "coap"
	TypeLwM2M = "lwm2m"
	TypeSNMP  = "snmp"
)

// Module is one southbound module row.
type Module struct {
	// ID is the stable module identifier.
	ID string `json:"id"`

	// Type is the protocol family: coap, lwm2m or snmp.
	Type string `json:"type"`

	// Name is the unique human-facing module name.
	Name string `json:"name"`

	// IngressID identifies the dispatch subject of the module.
	IngressID string `json:"ingress_id"`

	// Enabled gates traffic: a disabled module rejects new sessions.
	Enabled bool `json:"enabled"`

	// Config carries protocol-specific parameters.
	Config json.RawMessage `json:"config"`

	// Version is the adapter build version reported at registration.
	Version string `json:"version"`

	// Status is the heartbeat-derived state: ONLINE or OFFLINE.
	Status string `json:"status"`

	// EnrollmentToken authenticates the adapter registration. It is only
	// included in management responses, never in adapter responses.
	EnrollmentToken string `json:"enrollment_token,omitempty"`

	// RegisteredAt is the first successful registration time.
	RegisteredAt *time.Time `json:"registered_at"`

	// LastHeartbeat is the latest lease renewal time.
	LastHeartbeat *time.Time `json:"last_heartbeat"`

	// CreatedAt is the row creation time.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the last row update time.
	UpdatedAt time.Time `json:"updated_at"`
}

// RegisterInput is the adapter registration request.
type RegisterInput struct {
	// EnrollmentToken is the token provisioned for the module.
	EnrollmentToken string

	// Type must match the provisioned module type.
	Type string

	// IngressID must match the provisioned dispatch identity.
	IngressID string

	// Version is the adapter build version.
	Version string
}

// HeartbeatLease flips modules with no heartbeat within the lease window to
// OFFLINE.
type HeartbeatLease struct {
	// Window bounds how long after the last heartbeat a module stays ONLINE.
	Window time.Duration

	// SweepInterval paces the sweeper.
	SweepInterval time.Duration
}
