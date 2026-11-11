// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package connectorcatalog stores the JSON Schemas an edge registers for the
// connector types it supports and validates connector configuration against
// them (ADR-0026 deferred slice: edge-registered config-schema validation).
//
// The kernel still understands no connector protocol: a schema is opaque
// JSONB compiled by a draft 2020-12 engine. A negotiated gateway advertises
// an inline config_schema per (connector_type, connector_type_version) in
// hello; NL upserts it into connector_schema (migration 0043). When a profile
// template or a connector instance is written, its config document is
// validated against the registered schema for the resolved type/version.
//
// Validation is best-effort by design: when no schema is registered for a
// type/version, the config is accepted, so unmanaged connector types and
// deployments without a registering edge keep working unchanged.
package connectorcatalog

import (
	"encoding/json"
	"errors"
	"time"
)

// SourceEdge marks a schema delivered by an edge hello (the only source
// today; a future operator REST catalog would add its own source value).
const SourceEdge = "edge"

// ErrInvalidSchema means the advertised JSON Schema itself cannot be compiled.
var ErrInvalidSchema = errors.New("connector config schema is invalid")

// ErrConfigViolation means a config document fails its registered schema.
var ErrConfigViolation = errors.New("connector config violates its schema")

// Registration is one inline schema advertised by an edge in hello.
type Registration struct {
	// ConnectorType is the connector type key, e.g. "modbus-tcp".
	ConnectorType string `json:"type"`

	// ConnectorTypeVersion is the type/plugin version. Empty registers an
	// unversioned ("any") schema used when a spec omits type_version.
	ConnectorTypeVersion string `json:"type_version"`

	// ConfigSchema is the inline JSON Schema (draft 2020-12) for the
	// connector config document.
	ConfigSchema json.RawMessage `json:"config_schema"`

	// Protocol is the optional open-vocabulary protocol family this
	// connector belongs to ("modbus", ...). Empty means undeclared; family
	// checks then fail open (ADR-0032 §6).
	Protocol string `json:"protocol,omitempty"`

	// Transports optionally lists the concrete transport keys inside the
	// family ("modbus-tcp","modbus-rtu"). Empty when not applicable.
	Transports []string `json:"transports,omitempty"`
}

// Schema is one stored catalog row.
type Schema struct {
	ID                   string          `json:"id"`
	TenantID             string          `json:"tenant_id"`
	ConnectorType        string          `json:"connector_type"`
	ConnectorTypeVersion string          `json:"connector_type_version"`
	ConfigSchema         json.RawMessage `json:"config_schema"`
	// Protocol is the declared protocol family ("" unknown/fail-open).
	Protocol string `json:"protocol,omitempty"`
	// Transports is the concrete transport-key list inside the family.
	Transports   []string  `json:"transports,omitempty"`
	Source       string    `json:"source"`
	RegisteredBy string    `json:"registered_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SchemaResult reports the outcome of registering one advertised schema.
type SchemaResult struct {
	// ConnectorType and ConnectorTypeVersion echo the advertisement.
	ConnectorType        string `json:"type"`
	ConnectorTypeVersion string `json:"type_version"`

	// Accepted reports whether the schema compiled and was stored.
	Accepted bool `json:"accepted"`

	// Reason explains a rejected schema (compile error).
	Reason string `json:"reason,omitempty"`
}
