// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package opcatalog stores the descriptors for the immediate gateway-level
// operations a gateway supports (ADR-0035). The kernel has no built-in
// operation vocabulary: operation keys, risk levels, interaction kinds and
// parameter/result schemas are data. Each gateway advertises its own copy in
// hello (table gateway_operation_advertisement, one row per
// gateway+operation+version, reconciled as a full set), descriptors can be
// defined per tenant (table tenant_operation), and a key can be disabled per
// tenant (table tenant_operation_disabled).
//
// Invocation resolves the descriptor the TARGET GATEWAY itself advertised
// (newest version), falling back to a tenant-private descriptor. The console
// catalog is a tenant-scoped aggregate over the per-gateway rows: one row per
// operation+version, listing the gateways advertising it and flagging
// divergent contracts.
package opcatalog

import (
	"encoding/json"
	"errors"
)

// Interaction kinds.
const (
	// InteractionRequestResponse waits for an edge result.
	InteractionRequestResponse = "request-response"
	// InteractionOneWay does not wait for a result.
	InteractionOneWay = "one-way"
)

// Risk levels.
const (
	RiskRead      = "read"
	RiskNormal    = "normal"
	RiskDangerous = "dangerous"
)

// Sentinel errors.
var (
	// ErrNotFound means no descriptor exists for the key.
	ErrNotFound = errors.New("gateway operation not found")
	// ErrInvalidDescriptor means an advertisement/write failed validation.
	ErrInvalidDescriptor = errors.New("gateway operation descriptor is invalid")
	// ErrConflict means a private descriptor already uses the key.
	ErrConflict = errors.New("gateway operation already exists")
	// ErrInvalidParams means invocation params failed the param schema.
	ErrInvalidParams = errors.New("gateway operation params are invalid")
)

// Descriptor is one operation descriptor, shared by global and tenant rows.
type Descriptor struct {
	Operation    string            `json:"operation"`
	TypeVersion  string            `json:"type_version"`
	Interaction  string            `json:"interaction"`
	TimeoutMs    int               `json:"timeout_ms"`
	ParamSchema  json.RawMessage   `json:"param_schema,omitempty"`
	ResultSchema json.RawMessage   `json:"result_schema,omitempty"`
	Risk         string            `json:"risk"`
	DisplayName  map[string]string `json:"display_name,omitempty"`
	Description  map[string]string `json:"description,omitempty"`
}

// Registration is one descriptor advertised in hello.
type Registration = Descriptor

// Result reports a hello registration outcome.
type Result struct {
	Operation   string `json:"operation"`
	TypeVersion string `json:"type_version"`
	Accepted    bool   `json:"accepted"`
	Reason      string `json:"reason,omitempty"`
}

// Advertiser identifies one gateway that advertises an aggregated operation.
type Advertiser struct {
	// ID is the gateway device id.
	ID string `json:"id"`
	// Name is the gateway canonical name.
	Name string `json:"name"`
}

// View is a descriptor in an effective tenant catalog, annotated with its
// origin and disabled state. Edge aggregate views additionally carry the
// advertising gateways and a conflict flag when same-key advertisers expose
// divergent contracts.
type View struct {
	Descriptor
	Source   string `json:"source"`
	Disabled bool   `json:"disabled"`
	// AdvertisedBy lists the advertising gateways for an aggregate edge row.
	AdvertisedBy []Advertiser `json:"advertised_by,omitempty"`
	// GatewayCount is len(AdvertisedBy), duplicated for convenient rendering.
	GatewayCount int `json:"gateway_count,omitempty"`
	// Conflict marks an edge key whose advertisers disagree on the contract.
	Conflict bool `json:"conflict,omitempty"`
}
