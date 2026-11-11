// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package companion implements the platform-neutral connector for the
// companion-subscription contract (companion-subscription/v1). It is the
// single owner implementing business.CompanionOwner: adding a new companion
// application requires registering an endpoint and conforming to the
// contract, never new NL code. NL stores subscription intent and read-back
// status only; companion domain data (galleries, ledgers, persons,
// embeddings) never enters these tables. See
// NereidOverview/docs/contracts/companion-subscription-v1.md.
package companion

import (
	"errors"
	"time"
)

// ContractV1 is the first versioned companion subscription contract.
const ContractV1 = "companion-subscription/v1"

// Wire and lifecycle constants shared with every conformant companion.
const (
	headerContract = "X-Nereid-Companion-Contract"
	healthzPath    = "/healthz"
	versionPath    = "/api/v1/version"
	subsPath       = "/api/v1/subscriptions"

	// StatusApplied and StatusFailed mirror the companion read-back vocabulary.
	StatusApplied = "APPLIED"
	StatusFailed  = "FAILED"
	// StatusPending is the NL-side state before the first APPLIED read-back.
	StatusPending = "PENDING"
)

// ErrEndpointNotFound is returned when no registered endpoint matches the
// artifact's companion id and gateway. The owner wraps it with
// business.ErrInvalid so deployment fails closed at preflight/apply time.
var ErrEndpointNotFound = errors.New("companion endpoint not registered")

// ErrContractMismatch means the companion installation does not advertise
// the contract version the endpoint was registered for. No subscription
// write is attempted; the deployment issue surfaces the companion's version.
var ErrContractMismatch = errors.New("companion contract version mismatch")

// ErrUpstreamError wraps a non-2xx response from a companion. The previous
// desired set stays valid and the failure is reported as a blocking issue.
var ErrUpstreamError = errors.New("companion upstream error")

// Endpoint is one registered companion installation. It is tenant-scoped
// and selected by the template's companion id plus the target gateway.
type Endpoint struct {
	ID                 string
	TenantID           string
	CompanionID        string
	GatewayID          string
	BaseURL            string
	ContractVersion    string
	AuthToken          string
	InsecureSkipVerify bool
	Enabled            bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// DesiredRow is one subscription row NL desires on one endpoint. RowID is
// deterministic so re-apply never duplicates a companion-local row.
type DesiredRow struct {
	RowID              string
	SourceDevice       string
	Camera             string
	PipelineID         string
	ConclusionDeviceID string
	Enabled            bool
	ArtifactKey        string
	Revision           int64
	Status             string
	Reason             string
}

// ReadbackRow is the companion-reported status for one row.
type ReadbackRow struct {
	ID       string
	Revision int64
	Status   string
	Reason   string
}

// wireSubscription is the contract request/GET item shape. Field closure is
// enforced by the companion; NL sends exactly these six fields.
type wireSubscription struct {
	ID                 string `json:"id"`
	SourceDevice       string `json:"sourceDevice"`
	Camera             string `json:"camera"`
	PipelineID         string `json:"pipelineId"`
	ConclusionDeviceID string `json:"conclusionDeviceId"`
	Enabled            bool   `json:"enabled"`
}

type wirePutRequest struct {
	Subscriptions []wireSubscription `json:"subscriptions"`
}

// wirePutResponse accepts the reference companion's {"items":[...]} shape.
type wirePutResponse struct {
	Items []wireReadback `json:"items"`
}

type wireReadback struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

type wireVersion struct {
	Service    string `json:"service"`
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}
