// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// principal.go defines the resolved caller and the authorization
// decision: does the principal hold a point for a tenant.

package rbac

// Data scope constants, matching migration 0014 CHECK values.
const (
	// ScopeTenant means every resource of the tenant is visible.
	ScopeTenant = "TENANT"

	// ScopeCustomer limits visibility to one customer subtree.
	ScopeCustomer = "CUSTOMER"

	// ScopeGroup limits visibility to one device group subtree.
	ScopeGroup = "GROUP"

	// ScopeSelf limits visibility to resources owned by the user.
	ScopeSelf = "SELF"
)

// TenantGrant is one membership projection: scope plus roles.
type TenantGrant struct {
	// Scope is the data scope of the membership.
	Scope string `json:"scope"`

	// ScopeID anchors CUSTOMER/GROUP; empty for TENANT/SELF.
	ScopeID string `json:"scope_id,omitempty"`

	// Roles are the built-in role keys of the membership.
	Roles []string `json:"roles"`
}

// Principal is the authenticated caller.
type Principal struct {
	// UserID identifies the account.
	UserID string `json:"user_id"`

	// Email is the login identifier.
	Email string `json:"email"`

	// PlatformAdmin marks a cross-tenant platform administrator.
	PlatformAdmin bool `json:"platform_admin"`

	// Tenants maps tenant ID to its grant; absence means no access.
	Tenants map[string]TenantGrant `json:"tenants"`

	// Machine marks an OAuth2 service-account principal. Its grant is a
	// single tenant and authorization reads Scopes, never membership roles.
	Machine bool `json:"machine,omitempty"`

	// Scopes are the ADR-0022 permission points carried by a machine
	// token or an explicitly scoped user API key; empty for ordinary
	// human principals and inheritance-mode API keys.
	Scopes []string `json:"scopes,omitempty"`

	// APIKeyID identifies the long-lived user credential backing a
	// human principal. Empty for interactive sessions and machine tokens.
	APIKeyID string `json:"api_key_id,omitempty"`
}

// HasPoint reports whether the principal may exercise point inside
// tenant. Membership scope narrowing (customer/group) is applied at
// resource granularity later; at the route level a membership in the
// tenant with the point is the decision.
func (p *Principal) HasPoint(tenant, point string) bool {
	// An explicitly scoped user API key remains a human principal for
	// membership and data-scope purposes, but authorizes only from its
	// validated point subset. An empty scope list inherits live roles.
	if p.APIKeyID != "" && len(p.Scopes) > 0 {
		if _, ok := p.Tenants[tenant]; !ok {
			return false
		}
		for _, granted := range p.Scopes {
			if granted == PointAll || granted == point {
				return true
			}
		}
		return false
	}

	// Machine principals authorize exclusively from the token's scope
	// list, never from a role set. A machine token is single-tenant, so it
	// must also hold a grant for exactly the requested tenant; the
	// SERVICE_ACCOUNT role is a label for audit, not a source of points
	// (its rolePoints entry is intentionally empty).
	if p.Machine {
		if _, ok := p.Tenants[tenant]; !ok {
			return false
		}
		// The wildcard never extends to identity administration: a
		// machine principal must not be able to mint other machine
		// principals. The gateway independently rejects machine callers
		// on service-account routes; this is the authorization-layer
		// half of the same invariant.
		if point == PointServiceAccountRead || point == PointServiceAccountWrite {
			return false
		}
		for _, granted := range p.Scopes {
			if granted == PointAll || granted == point {
				return true
			}
		}
		return false
	}
	grant, ok := p.Tenants[tenant]
	if !ok {
		return false
	}
	for _, role := range grant.Roles {
		for _, granted := range rolePoints[role] {
			if granted == PointAll || granted == point {
				return true
			}
		}
	}
	return false
}

// IsMember reports any membership of the tenant regardless of point;
// used for routes whose exact point is derived after authentication.
func (p *Principal) IsMember(tenant string) bool {
	_, ok := p.Tenants[tenant]
	return ok
}

// HasPointAny reports the point in at least one tenant grant. It is
// the WebSocket subscription check where the device-to-tenant lookup
// happens through the data path rather than the subscription frame.
func (p *Principal) HasPointAny(point string) bool {
	for tenant := range p.Tenants {
		if p.HasPoint(tenant, point) {
			return true
		}
	}
	return false
}
