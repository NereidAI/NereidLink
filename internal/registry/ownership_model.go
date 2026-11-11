// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ownership_model.go defines the data types for the ownership and
// lifecycle model from ADR-0013: customers, device groups, claiming
// tokens, transfer requests and the append-only ownership history.

package registry

// Lifecycle state constants, matching the device.state CHECK constraint.
const (
	// StateUnclaimed is a pre-registered device awaiting claiming.
	StateUnclaimed = "UNCLAIMED"

	// StateActive is a normal, usable device.
	StateActive = "ACTIVE"

	// StateDecommissioned is a retired device inside its recall window.
	StateDecommissioned = "DECOMMISSIONED"

	// StateArchived is reserved for the later cold-archive stage.
	StateArchived = "ARCHIVED"

	// StateQuarantined is an incident-response state blocking all
	// connections until release.
	StateQuarantined = "QUARANTINED"
)

// Claim status constants, matching the device_claim CHECK constraint.
const (
	// ClaimIssued is an unused, non-expired claim token.
	ClaimIssued = "ISSUED"

	// ClaimClaimed is a consumed token.
	ClaimClaimed = "CLAIMED"

	// ClaimExpired is a token whose expiry passed before use.
	ClaimExpired = "EXPIRED"

	// ClaimRevoked is an administratively canceled token.
	ClaimRevoked = "REVOKED"
)

// Ownership event type constants, matching the CHECK constraint.
const (
	// OwnershipRegistered is device creation.
	OwnershipRegistered = "registered"

	// OwnershipClaimed is a successful claim.
	OwnershipClaimed = "claimed"

	// OwnershipTransferred is an ownership move between customers/groups.
	OwnershipTransferred = "transferred"

	// OwnershipDecommissioned is retirement.
	OwnershipDecommissioned = "decommissioned"

	// OwnershipReactivated is restoration inside the recall window.
	OwnershipReactivated = "reactivated"

	// OwnershipArchived is the terminal cold-archive stage, reached when a
	// recall window closes without the device being restored.
	OwnershipArchived = "archived"
)

// Alarm policy choices for ownership transfer.
const (
	// AlarmPolicyRetain carries unresolved alarms with the device.
	AlarmPolicyRetain = "retain"

	// AlarmPolicyClear closes live alarms during transfer.
	AlarmPolicyClear = "clear"
)

// Credential status constants, matching the device_credential CHECK.
const (
	// CredentialActive authenticates normally inside its validity window.
	CredentialActive = "ACTIVE"

	// CredentialDisabled is administratively switched off.
	CredentialDisabled = "DISABLED"

	// CredentialExpired passed its expiry time.
	CredentialExpired = "EXPIRED"
)

// Expiry warning levels, strongest last. The scanner raises each at most
// once per credential as the deadline approaches.
const (
	// Warn30d is raised 30 days before expiry.
	Warn30d = "30d"

	// Warn7d is raised 7 days before expiry.
	Warn7d = "7d"

	// Warn1d is raised 1 day before expiry.
	Warn1d = "1d"
)

// CustomerDTO is the management representation of a customer.
type CustomerDTO struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name"`
	ContactName  string `json:"contact_name,omitempty"`
	ContactEmail string `json:"contact_email,omitempty"`
	ExternalID   string `json:"external_id,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// GroupDTO is the management representation of a device group node.
type GroupDTO struct {
	ID         string `json:"id,omitempty"`
	CustomerID string `json:"customer_id,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// ClaimTokenResult is returned once at token issuance. The plaintext
// token is never stored and cannot be recovered later.
type ClaimTokenResult struct {
	// DeviceID identifies the claimable device.
	DeviceID string `json:"device_id"`

	// Token is the single-use plaintext claim token.
	Token string `json:"token"`

	// ExpiresAt is the token deadline as RFC3339 UTC.
	ExpiresAt string `json:"expires_at"`
}

// ClaimInput is the claiming request.
type ClaimInput struct {
	// Token authenticates the claim.
	Token string `json:"token"`

	// CustomerID is the destination customer; optional.
	CustomerID string `json:"customer_id,omitempty"`

	// GroupID is the destination group; optional.
	GroupID string `json:"group_id,omitempty"`
}

// TransferInput is an ownership move request.
type TransferInput struct {
	// CustomerID is the destination customer.
	CustomerID string `json:"customer_id"`

	// GroupID is the destination group; optional.
	GroupID string `json:"group_id,omitempty"`

	// ResetCredentials deletes existing credentials during transfer.
	ResetCredentials bool `json:"reset_credentials"`

	// AlarmPolicy is retain or clear.
	AlarmPolicy string `json:"alarm_policy"`
}

// OwnershipEventDTO is one append-only ownership history row.
type OwnershipEventDTO struct {
	ID             string         `json:"id"`
	DeviceID       string         `json:"device_id"`
	EventType      string         `json:"event_type"`
	FromCustomerID string         `json:"from_customer_id,omitempty"`
	ToCustomerID   string         `json:"to_customer_id,omitempty"`
	FromGroupID    string         `json:"from_group_id,omitempty"`
	ToGroupID      string         `json:"to_group_id,omitempty"`
	ActorID        string         `json:"actor_id,omitempty"`
	Details        map[string]any `json:"details"`
	CreatedAt      string         `json:"created_at"`
}
