// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package protocolcatalog stores the GLOBAL, operator-editable catalog of
// protocol families / reference EdgeX device services (ADR-0033, migration
// 0063). Each row describes, for one family:
//
//   - the concrete transport keys a Device.Protocols map may use,
//   - the structured connection parameters rendered in the console connector
//     form and emitted verbatim into the edge protocol-property map,
//   - the per-point address parameters rendered in the point-descriptor form
//     and emitted into a resource's opaque address object.
//
// The platform kernel still parses no protocol: these descriptors drive UI
// forms and suggestions only and ingestion never consults them. They are
// global vendor facts (no tenant scope); the tenant segment of the REST path
// supplies authorization. The fourteen rows matching the EdgeX 4.0.2
// device-service index are seeded builtin and may be corrected but not
// deleted; operator-added custom families are fully removable.
package protocolcatalog

import (
	"encoding/json"
	"errors"
	"time"
)

// Source marks how a catalog row was created.
const (
	// SourceOfficial is a row seeded from the EdgeX reference services.
	SourceOfficial = "official"
	// SourceCustom is an operator-defined family.
	SourceCustom = "custom"
)

// Sentinel errors mapped to HTTP status by the handler.
var (
	// ErrNotFound means no catalog row exists for the family.
	ErrNotFound = errors.New("protocol family not found")
	// ErrConflict means the family key already exists.
	ErrConflict = errors.New("protocol family already exists")
	// ErrInvalid means the submitted family document failed validation.
	ErrInvalid = errors.New("protocol family is invalid")
	// ErrBuiltin means a builtin family cannot be deleted.
	ErrBuiltin = errors.New("built-in protocol family cannot be deleted")
)

// validKinds is the closed set of structured field control types.
var validKinds = map[string]bool{
	"string": true, "integer": true, "number": true,
	"boolean": true, "duration": true, "select": true,
}

// FieldOption is one selectable value for a "select" field.
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// FieldSpec describes one structured connection or point-address parameter.
// Key is emitted verbatim into the JSON document the edge consumes, so its
// casing must match the reference device service exactly.
type FieldSpec struct {
	// Key is the JSON property name sent to the edge.
	Key string `json:"key"`
	// Label is the human label shown in the console (catalog data is global,
	// so labels are literal strings rather than i18n keys).
	Label string `json:"label"`
	// Kind selects the edit control.
	Kind string `json:"kind"`
	// Required marks the parameter the reference service needs to connect.
	Required bool `json:"required,omitempty"`
	// Placeholder is an illustrative value, never a stored default.
	Placeholder string `json:"placeholder,omitempty"`
	// Hint is optional secondary help text.
	Hint string `json:"hint,omitempty"`
	// Options backs a select control.
	Options []FieldOption `json:"options,omitempty"`
	// Transports lists the transport keys the field applies to; ["*"] means
	// every transport of the family. Connection fields only.
	Transports []string `json:"transports,omitempty"`
	// Min/Max bound numeric fields.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// DefaultValue is inserted into a freshly created point address.
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
}

// Transport is one concrete device-level protocol key (a Device.Protocols
// map key) for a family.
type Transport struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Family is one stored catalog row.
type Family struct {
	Family              string          `json:"family"`
	DisplayName         string          `json:"display_name"`
	ServiceName         string          `json:"service_name"`
	ProtocolKey         string          `json:"protocol_key"`
	Transports          []Transport     `json:"transports"`
	ConnectionFields    []FieldSpec     `json:"connection_fields"`
	PointFields         []FieldSpec     `json:"point_fields"`
	DefaultPointAddress json.RawMessage `json:"default_point_address"`
	DocURL              string          `json:"doc_url"`
	Builtin             bool            `json:"builtin"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// SaveInput is the create/update payload. The family key and builtin flag are
// never taken from the body on update (the key is the path; builtin is
// immutable), so an operator cannot turn a custom row into a protected one.
type SaveInput struct {
	Family              string          `json:"family"`
	DisplayName         string          `json:"display_name"`
	ServiceName         string          `json:"service_name"`
	ProtocolKey         string          `json:"protocol_key"`
	Transports          []Transport     `json:"transports"`
	ConnectionFields    []FieldSpec     `json:"connection_fields"`
	PointFields         []FieldSpec     `json:"point_fields"`
	DefaultPointAddress json.RawMessage `json:"default_point_address,omitempty"`
	DocURL              string          `json:"doc_url"`
}
