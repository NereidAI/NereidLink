// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
)

var (
	// ErrNotFound means the requested registry object does not exist.
	ErrNotFound = errors.New("registry object not found")

	// ErrConflict means a unique identifier or name is already in use.
	ErrConflict = errors.New("registry object conflict")

	// ErrInvalidArgument means a request field is invalid.
	ErrInvalidArgument = errors.New("invalid registry argument")
)

// Profile source values (ADR-0032 device_profile.source).
const (
	// SourceEdge: content auto-ingested from a gateway uplink.
	SourceEdge = "edge"
	// SourceImport: content created from a pasted neutral/EdgeX profile.
	SourceImport = "import"
	// SourceManual: content authored in the console (default).
	SourceManual = "manual"
)

// Profile availability values (ADR-0032 device_profile.availability).
const (
	// AvailabilityDiscovered: reported/imported but not referenced yet.
	AvailabilityDiscovered = "discovered"
	// AvailabilityActive: referenced by a device, connector instance or
	// carrying its own connector template.
	AvailabilityActive = "active"
)

// DescriptorDTO is the management representation of a measurement descriptor.
type DescriptorDTO struct {
	ID             string            `json:"id,omitempty"`
	MeasurementKey string            `json:"measurement_key"`
	Unit           string            `json:"unit,omitempty"`
	TransformType  string            `json:"transform_type"`
	Scale          float64           `json:"scale"`
	Offset         float64           `json:"offset"`
	Breakpoints    []BreakpointDTO   `json:"breakpoints,omitempty"`
	EnumMap        map[string]string `json:"enum_map,omitempty"`
	Expression     string            `json:"expression,omitempty"`
	Precision      int               `json:"precision"`
	ValidMin       *float64          `json:"valid_min,omitempty"`
	ValidMax       *float64          `json:"valid_max,omitempty"`
	Version        int               `json:"version,omitempty"`

	// ExpectedPeriodSeconds declares how often this point is expected to
	// report (ADR-0014). NULL/absent means the point is excluded from
	// online-but-silent detection; a positive value enables it.
	ExpectedPeriodSeconds *int `json:"expected_period_seconds,omitempty"`
	// ToleranceMultiplier widens the silence deadline to absorb normal
	// jitter: deadline = expected_period_seconds * tolerance_multiplier.
	// Zero is treated as the default of 3.
	ToleranceMultiplier float64 `json:"tolerance_multiplier,omitempty"`

	// Neutral spec fields (ADR-0032) describe the point as reported by the
	// edge / import. They are distinct from the NL value-add calibration
	// fields above: edge re-reports update these and never touch transform /
	// scale / enum / valid range.
	//
	// DataType is the neutral enum bool|int|uint|float|string|binary.
	DataType string `json:"data_type,omitempty"`
	// ReadWrite is the access mode "R", "RW" or "" when unknown.
	ReadWrite string `json:"read_write,omitempty"`
	// Address is the opaque per-protocol addressing object (Modbus
	// {primaryTable,startingAddress}, OPC-UA {nodeId}, ...). The kernel never
	// parses it; it is emitted as {} when empty, never null.
	Address json.RawMessage `json:"address,omitempty"`
	// EdgeDefault is the reported default value as text.
	EdgeDefault string `json:"edge_default,omitempty"`
	// EdgeMin/EdgeMax are the reported engineering range, distinct from the
	// operator-owned validation range ValidMin/ValidMax.
	EdgeMin *float64 `json:"edge_min,omitempty"`
	EdgeMax *float64 `json:"edge_max,omitempty"`
	// Inactive marks a point the edge removed; the descriptor row is kept for
	// historical data and rule references rather than deleted.
	Inactive bool `json:"inactive,omitempty"`
}

// BreakpointDTO is one non-linear conversion point.
type BreakpointDTO struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ProfileDTO is the management representation of a device profile.
type ProfileDTO struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name"`
	RuleChainGroupID string `json:"rule_chain_group_id,omitempty"`
	// Descriptors is always emitted as an array, even when empty, so API
	// clients can map over it without null checks.
	Descriptors []DescriptorDTO `json:"descriptors"`

	// ConnectorTemplate optionally turns this profile into a connector
	// template (ADR-0026 two-level config). ConnectorType empty means the
	// profile carries telemetry descriptors only. The config/mappings JSON
	// is opaque to the kernel and is merged under per-instance overrides at
	// dispatch time. The maps are emitted as []/{}, never null.
	ConnectorType           string          `json:"connector_type,omitempty"`
	ConnectorTypeVersion    string          `json:"connector_type_version,omitempty"`
	ConnectorConfigTemplate json.RawMessage `json:"connector_config_template"`
	ConnectorMappings       json.RawMessage `json:"connector_mappings"`

	// Profile-library metadata (ADR-0032). Source records where the content
	// came from: "edge" (gateway uplink), "import" (paste import) or
	// "manual". Protocol is the open-vocabulary protocol family ("" unknown).
	// EdgePayload keeps the verbatim reported/imported neutral profile JSON;
	// ContentHash is its canonical sha256 for drift detection. Availability is
	// "discovered" (only reported, unreferenced) or "active".
	Source       string          `json:"source,omitempty"`
	Protocol     string          `json:"protocol,omitempty"`
	EdgePayload  json.RawMessage `json:"edge_payload,omitempty"`
	ContentHash  string          `json:"content_hash,omitempty"`
	Availability string          `json:"availability,omitempty"`
}

// DeviceDTO is the management representation of a device.
type DeviceDTO struct {
	ID               string `json:"id,omitempty"`
	CanonicalName    string `json:"canonical_name"`
	ProfileID        string `json:"profile_id,omitempty"`
	RuleChainGroupID string `json:"rule_chain_group_id,omitempty"`

	// CustomerID owns the device; empty means unassigned.
	CustomerID string `json:"customer_id,omitempty"`

	// GroupID assigns the device to a device group.
	GroupID string `json:"group_id,omitempty"`

	// IsGateway reports that the device may host passive sub-devices.
	IsGateway bool `json:"is_gateway"`

	// GatewayID identifies the gateway authorized for this sub-device.
	GatewayID string `json:"gateway_id,omitempty"`

	// GatewayName is the canonical name of the owning gateway, resolved
	// when the device is a sub-device.
	GatewayName string `json:"gateway_name,omitempty"`

	// ConnectorID is the spec id of the connector instance bound to this
	// sub-device (one per device, enforced by a partial unique index).
	// Empty when the device has no bound connector or is itself a gateway.
	ConnectorID string `json:"connector_id,omitempty"`

	// ConnectorType is the bound instance's connector type.
	ConnectorType string `json:"connector_type,omitempty"`

	// ConnectorState is the bound instance's last reported desired/apply
	// state (PENDING, DISPATCHED, CREATING, APPLIED, FAILED, DEGRADED).
	ConnectorState string `json:"connector_state,omitempty"`

	// GatewayConnectorCount is the number of gateway-level connector
	// instances (device_id IS NULL) owned by this device. Populated only
	// for gateway rows; zero is omitted.
	GatewayConnectorCount int `json:"gateway_connector_count,omitempty"`

	// FirmwareVersion is the version the device last reported after a
	// successful OTA rollout (ADR-0006b). Empty when never reported.
	FirmwareVersion string `json:"firmware_version,omitempty"`

	// AgentFeatures is the feature set negotiated in the gateway's last
	// successful hello (ADR-0034). "secrets" marks an edge that accepts
	// secret delivery. Empty for non-gateways.
	AgentFeatures []string `json:"agent_features,omitempty"`

	// AgentOperations is the operation keys the gateway advertised in its
	// last successful hello (ADR-0035). Used to scope the gateway
	// Operations tab; tenant-defined descriptors are always offered.
	AgentOperations []string `json:"agent_operations,omitempty"`

	// State is the lifecycle state: UNCLAIMED, ACTIVE, DECOMMISSIONED or
	// ARCHIVED. Normal devices are ACTIVE.
	State string `json:"state"`

	// ClaimedAt records the first successful claim as RFC3339 UTC.
	ClaimedAt string `json:"claimed_at,omitempty"`

	// DecommissionedAt records the decommission boundary.
	DecommissionedAt string `json:"decommissioned_at,omitempty"`

	// RecallUntil bounds the reactivation window.
	RecallUntil string `json:"recall_until,omitempty"`

	// PresenceState is the latest boundary state (ONLINE/OFFLINE) derived
	// from ts_presence; empty when the device never connected.
	PresenceState string `json:"presence_state,omitempty"`

	// PresenceSince is when the current state began, RFC3339 UTC.
	PresenceSince string `json:"presence_since,omitempty"`

	// QuarantinedAt records when the device entered incident-response
	// quarantine, RFC3339 UTC. Empty when it is not quarantined.
	//
	// These two fields are the reason the device list can surface a
	// quarantined device at all: without them an operator sees the state
	// badge but not the incident that caused it, which is the only part
	// they need in order to decide on a release.
	QuarantinedAt string `json:"quarantined_at,omitempty"`

	// QuarantineReason is the stored reason: the structured code, optionally
	// followed by ": " and the free-text note.
	QuarantineReason string `json:"quarantine_reason,omitempty"`

	// CreatedAt is when the device row was created, RFC3339 UTC. It backs
	// the list's "Since" column (device creation time, not presence).
	CreatedAt string `json:"created_at,omitempty"`

	// UpdatedAt is when the device row was last edited (name/profile/alias/
	// notes/...), RFC3339 UTC.
	UpdatedAt string `json:"updated_at,omitempty"`

	// Alias is an optional operator-defined human-friendly display name.
	// Unlike canonical_name it carries no identity/credential meaning.
	Alias string `json:"alias,omitempty"`

	// Notes is optional free-form additional information about the device.
	Notes string `json:"notes,omitempty"`

	// LastActivityAt is the most recent device activity timestamp, RFC3339
	// UTC: the latest telemetry or presence boundary, whichever is later.
	// It is computed per request and never stored on the device row.
	LastActivityAt string `json:"last_activity_at,omitempty"`
}

// CredentialDTO is the management representation of a device credential.
type CredentialDTO struct {
	ID               string         `json:"id,omitempty"`
	Type             string         `json:"type"`
	CredentialsID    string         `json:"credentials_id"`
	CredentialsValue string         `json:"credentials_value,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`

	// Issue applies to X509 only: true asks the platform built-in CA to
	// issue a key and certificate; false expects a pasted certificate.
	Issue bool `json:"issue,omitempty"`

	// Issued material returned only on create/reveal responses for X509.
	CertificatePEM string `json:"certificate_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem,omitempty"`
	CAPEM          string `json:"ca_pem,omitempty"`

	// Status is the enable state: ACTIVE, DISABLED or EXPIRED.
	Status string `json:"status"`

	// EffectiveAt is the validity start as RFC3339 UTC; empty means now.
	EffectiveAt string `json:"effective_at,omitempty"`

	// ExpiresAt is the validity end as RFC3339 UTC; empty means never.
	ExpiresAt string `json:"expires_at,omitempty"`

	// WarnLevel is the strongest expiry warning raised so far.
	WarnLevel string `json:"warn_level,omitempty"`

	// Revealable reports whether CredentialsValue carries the stored
	// value. It is false only for PASSWORD rows held as a one-way
	// argon2id hash, whose plaintext cannot be recovered.
	Revealable *bool `json:"revealable,omitempty"`
}
