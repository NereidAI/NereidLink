// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package secretcatalog stores the descriptors for the credentials the
// platform delivers to gateways (ADR-0034 edge secret type catalog). A
// descriptor declares the fields one edge credential carries as a
// self-contained draft 2020-12 JSON Schema; the kernel itself recognizes no
// credential protocol (ONVIF or any other) and only compiles the schema and
// validates credential documents against it.
//
// Descriptors come from two sources: a negotiated gateway advertises them
// in hello (source='edge', registered independently inside each tenant), or
// a tenant operator creates them manually for non-advertising gateways.
// When no descriptor exists for a requested type, credential writes are
// accepted with an ad-hoc ("blank") shape, so the feature works with no
// preparation: see BlankType and the synthesized permissive descriptor.
package secretcatalog

import (
	"encoding/json"
	"errors"
	"time"
)

// Source marks how a descriptor was created.
const (
	// SourceEdge is a descriptor advertised by a gateway in hello.
	SourceEdge = "edge"
	// SourceManual is a tenant-defined descriptor.
	SourceManual = "manual"
)

// BlankType selects the ad-hoc descriptor used when no schema is registered
// for a requested type. Credential writes then carry arbitrary key/value
// pairs; the platform only marks which fields are secret.
const BlankType = ""

// ErrInvalidSchema means the advertised fields schema itself cannot compile.
var ErrInvalidSchema = errors.New("edge secret fields schema is invalid")

// ErrFieldsViolation means a credential document fails its registered schema.
var ErrFieldsViolation = errors.New("edge secret document violates its schema")

// ErrNotFound means no descriptor exists for the requested (type, version).
var ErrNotFound = errors.New("edge secret type not found")

// ErrConflict means a manual descriptor already exists with that key.
var ErrConflict = errors.New("edge secret type already exists")

// Registration is one descriptor advertised by an edge in hello.
type Registration struct {
	// SecretType is the credential type key, e.g. "onvif-basic".
	SecretType string `json:"type"`

	// TypeVersion is the descriptor version. Empty registers an
	// unversioned ("any") descriptor.
	TypeVersion string `json:"type_version"`

	// FieldsSchema is the self-contained draft 2020-12 schema for the
	// credential document.
	FieldsSchema json.RawMessage `json:"fields_schema"`

	// DisplayName optionally carries bilingual display names,
	// e.g. {"en":"…","zh":"…"}.
	DisplayName map[string]string `json:"display_name,omitempty"`
}

// SecretType is one stored descriptor row.
type SecretType struct {
	ID           string            `json:"id"`
	TenantID     string            `json:"tenant_id"`
	SecretType   string            `json:"secret_type"`
	TypeVersion  string            `json:"type_version"`
	FieldsSchema json.RawMessage   `json:"fields_schema"`
	DisplayName  map[string]string `json:"display_name,omitempty"`
	Source       string            `json:"source"`
	RegisteredBy string            `json:"registered_by,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// TypeResult reports the outcome of registering one advertised descriptor.
type TypeResult struct {
	// SecretType and TypeVersion echo the advertisement.
	SecretType  string `json:"type"`
	TypeVersion string `json:"type_version"`

	// Accepted reports whether the schema compiled and was stored.
	Accepted bool `json:"accepted"`

	// Reason explains a rejected descriptor (compile error).
	Reason string `json:"reason,omitempty"`
}
