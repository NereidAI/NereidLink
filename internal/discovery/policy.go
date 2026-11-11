// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy.go adds per-tenant and per-gateway auto-provisioning policy on top
// of the deployment-level default (migration 0045). Effective policy for one
// discovery frame resolves most-specific-first: a gateway override wins over
// the tenant default, which wins over the deployment YAML. Every value column
// is nullable: NULL means "inherit the next level", so an override may set
// just one field (e.g. the caps) while inheriting enabled/default_profile.

package discovery

import "context"

// Override carries the optional fields of one stored policy row. A nil pointer
// means "not set at this level / inherit"; a non-nil pointer to false/0/"" is
// an explicit value (and serializes as such on a PUT body).
type Override struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	MaxPerCycle    *int    `json:"max_per_cycle,omitempty"`
	MaxCandidates  *int    `json:"max_candidates,omitempty"`
	DefaultProfile *string `json:"default_profile,omitempty"`
	// ApprovalMode selects immediate create ("auto") or human review
	// ("review"). A nil pointer inherits the next level.
	ApprovalMode *string `json:"approval_mode,omitempty"`
}

// Approval modes.
const (
	// ApprovalAuto creates eligible candidates immediately (pre-0046
	// behavior).
	ApprovalAuto = "auto"
	// ApprovalReview parks would-create candidates in the pending queue.
	ApprovalReview = "review"
)

// Record is one discovery_policy row: either the tenant default (GatewayID
// empty) or a gateway override. Nil-valued fields inherit downward.
type Record struct {
	TenantID  string
	GatewayID string
	Override
}

// View is the effective policy surfaced over REST for one scope. It carries
// the resolved concrete values plus whether a stored override row exists at
// that scope.
type View struct {
	TenantID       string `json:"tenant_id"`
	GatewayID      string `json:"gateway_id,omitempty"`
	Enabled        bool   `json:"enabled"`
	MaxPerCycle    int    `json:"max_per_cycle"`
	MaxCandidates  int    `json:"max_candidates"`
	DefaultProfile string `json:"default_profile"`
	// ApprovalMode is the resolved "auto"|"review" mode.
	ApprovalMode string `json:"approval_mode"`
	// Overridden reports whether a policy row exists at this exact scope.
	Overridden bool `json:"overridden"`
}

// PolicyReader resolves the stored override rows for one frame. Satisfied by
// the PostgreSQL policy store; nil disables DB policy (deployment default).
type PolicyReader interface {
	// RowsFor returns the tenant-default row (nil when absent) and the
	// gateway-override row (nil when absent).
	RowsFor(ctx context.Context, tenantID, gatewayID string) (tenantRow, gatewayRow *Record, err error)
}

// PolicyAdmin adds override management to the reader. Satisfied by
// *PolicyStore; nil disables the REST policy surface.
type PolicyAdmin interface {
	PolicyReader
	SetTenant(ctx context.Context, tenantID string, in Override) (Record, error)
	SetGateway(ctx context.Context, tenantID, gatewayID string, in Override) (Record, error)
	DeleteTenant(ctx context.Context, tenantID string) error
	DeleteGateway(ctx context.Context, tenantID, gatewayID string) error
}

// apply overlays a stored record onto a concrete policy, field by field.
func apply(p *Policy, r *Record) {
	if r == nil {
		return
	}
	if r.Enabled != nil {
		p.Enabled = *r.Enabled
	}
	if r.MaxPerCycle != nil {
		p.MaxPerCycle = *r.MaxPerCycle
	}
	if r.MaxCandidates != nil {
		p.MaxCandidates = *r.MaxCandidates
	}
	if r.DefaultProfile != nil {
		p.DefaultProfile = *r.DefaultProfile
	}
	if r.ApprovalMode != nil {
		p.ApprovalMode = *r.ApprovalMode
	}
}

// EffectivePolicy folds the deployment fallback, tenant default and gateway
// override into the concrete policy for one frame. A nil reader yields the
// fallback unchanged. Result caps are re-defaulted when still non-positive so
// an operator override of 0 is never stored (CHECK) and missing config keeps
// the package defaults.
func EffectivePolicy(ctx context.Context, reader PolicyReader, fallback Policy, tenantID, gatewayID string) (Policy, error) {
	p := fallback
	if reader == nil {
		normalizeCaps(&p)
		return p, nil
	}
	tenantRow, gatewayRow, err := reader.RowsFor(ctx, tenantID, gatewayID)
	if err != nil {
		return fallback, err
	}
	apply(&p, tenantRow)
	apply(&p, gatewayRow)
	normalizeCaps(&p)
	return p, nil
}

// normalizeCaps fills the hard floor caps applied in New when a level left
// them unset/non-positive.
func normalizeCaps(p *Policy) {
	if p.MaxCandidates <= 0 {
		p.MaxCandidates = 1000
	}
	if p.MaxPerCycle <= 0 {
		p.MaxPerCycle = 100
	}
	if p.ApprovalMode != ApprovalAuto && p.ApprovalMode != ApprovalReview {
		p.ApprovalMode = ApprovalAuto
	}
}

// toView renders a concrete policy for one scope.
func toView(tenantID, gatewayID string, p Policy, overridden bool) View {
	return View{
		TenantID:       tenantID,
		GatewayID:      gatewayID,
		Enabled:        p.Enabled,
		MaxPerCycle:    p.MaxPerCycle,
		MaxCandidates:  p.MaxCandidates,
		DefaultProfile: p.DefaultProfile,
		ApprovalMode:   p.ApprovalMode,
		Overridden:     overridden,
	}
}
