// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// scope.go implements the third element of the ADR-0022 decision:
//
//	decision = permission point × resource type × data scope.
//
// HasPoint answers the point; this file answers "which rows". A principal's
// membership carries one data scope, and every device-derived query must be
// narrowed by it — a Customer User holding device:read still only sees the
// devices of their own customer. Route-level point checks alone would let a
// scoped member read the whole tenant, which is the exact hole this closes.
//
// The scope reaches the data layer through the request context rather than a
// parameter on every service signature, because the gateway already owns the
// tenant decision and the narrowing predicate has the same shape for every
// resource. Internal callers (rule engine, device ingress, background jobs)
// never set it and therefore run tenant-wide, which is the correct system
// level: only a human or machine principal is narrowed.

package rbac

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DataScope is the resolved, immutable narrowing for one tenant request.
//
// The zero value is treated as unrestricted, so a missing context value can
// only ever widen access to system level, never accidentally narrow it.
type DataScope struct {
	// unrestricted skips predicate generation entirely.
	unrestricted bool

	// TenantID is the tenant the scope was resolved against.
	TenantID string

	// Level is ScopeTenant, ScopeCustomer or ScopeGroup.
	Level string

	// CustomerID anchors a CUSTOMER scope.
	CustomerID string

	// GroupID anchors the root of a GROUP scope subtree.
	GroupID string

	// GroupIDs is the resolved GROUP subtree, anchor group included.
	// Resolving once per request keeps the SQL predicate a flat = ANY(...)
	// instead of running a recursive CTE on every query.
	GroupIDs []string
}

// UnrestrictedScope returns the tenant-wide scope used by platform
// administration, internal services and background work.
func UnrestrictedScope(tenantID string) DataScope {
	return DataScope{unrestricted: true, TenantID: tenantID, Level: ScopeTenant}
}

// Restricted reports whether this scope hides any row from the caller.
func (s DataScope) Restricted() bool {
	return !s.unrestricted && s.Level != ScopeTenant
}

// Scope returns the level constant, for logging and API echoing.
func (s DataScope) Scope() string {
	if s.unrestricted {
		return ScopeTenant
	}
	return s.Level
}

// ScopeIDs returns the anchoring ids of a narrowed scope, for API echoing.
func (s DataScope) ScopeIDs() []string {
	switch s.Level {
	case ScopeCustomer:
		if s.CustomerID != "" {
			return []string{s.CustomerID}
		}
	case ScopeGroup:
		return append([]string(nil), s.GroupIDs...)
	}
	return []string{}
}

// dataScopeContext carries the resolved scope of the current request.
type dataScopeContext struct{}

// ContextWithScope marks ctx with the caller's tenant data scope. The
// gateway calls this once per authenticated request; everything downstream
// observes the same narrowing.
func ContextWithScope(ctx context.Context, scope DataScope) context.Context {
	return context.WithValue(ctx, dataScopeContext{}, scope)
}

// ScopeFromContext returns the request scope. A missing value yields the
// unrestricted scope: internal callers are not narrowed.
func ScopeFromContext(ctx context.Context) DataScope {
	if scope, ok := ctx.Value(dataScopeContext{}).(DataScope); ok {
		return scope
	}
	return DataScope{unrestricted: true, Level: ScopeTenant}
}

// DevicePredicate renders the SQL fragment narrowing a query that selects
// FROM device (optionally aliased), given how many bind parameters the host
// query already uses. It returns "" and no arguments when the scope narrows
// nothing, so callers can append both unconditionally.
//
// It appends exactly two parameters, the customer id and the group subtree,
// because the tenant is already constrained by the host query's own tenant
// predicate. The clause is an OR of the two ownership arms, so a device stays
// reachable through either its customer or its group. The arm a scope level
// does not use is disabled by NULLIF rather than by a leading guard: a guard
// of the form `($1 = ” OR <match>)` would make the whole predicate true for
// the unused arm and silently widen a GROUP scope back to the entire tenant.
// NULLIF yields NULL instead, which no row satisfies.
func (s DataScope) DevicePredicate(alias string, existing int) (string, []any) {
	if !s.Restricted() {
		return "", nil
	}
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	clause := fmt.Sprintf(
		" AND (%[1]scustomer_id = NULLIF($%[2]d,'')::uuid"+
			" OR %[1]sgroup_id = ANY($%[3]d::uuid[]))",
		prefix, existing+1, existing+2)
	return clause, s.ownershipArgs()
}

// DeviceSubqueryPredicate renders the SQL fragment narrowing a query on a
// table that carries a device_id but no ownership columns (alarm,
// telemetry, command, presence, ota item...). Visibility is defined by
// membership in the visible device set.
//
// It appends three parameters, because the membership subquery needs its own
// tenant predicate: the host query constrains the outer table by tenant, but
// a crafted device id must not be able to point the subquery at another
// tenant's device. Every generated parameter must be referenced by the
// returned SQL, because PostgreSQL cannot infer a type for an unreferenced
// placeholder and fails the whole query (SQLSTATE 42P18).
func (s DataScope) DeviceSubqueryPredicate(deviceIDCol string, existing int) (string, []any) {
	if !s.Restricted() {
		return "", nil
	}
	clause := fmt.Sprintf(
		" AND %[1]s IN (SELECT id FROM device WHERE tenant_id = $%[4]d"+
			" AND (customer_id = NULLIF($%[2]d,'')::uuid"+
			" OR group_id = ANY($%[3]d::uuid[])))",
		deviceIDCol, existing+1, existing+2, existing+3)
	args := append(s.ownershipArgs(), s.TenantID)
	return clause, args
}

// ownershipArgs returns the two ownership parameters in their fixed order:
// the customer anchor, then the group subtree.
//
// Only the arm the current level uses carries a value; the other is bound to
// something that matches nothing, which is what disables it. Both parameters
// must always be returned even when unused, because the generated clause
// references both placeholders.
func (s DataScope) ownershipArgs() []any {
	customer := ""
	groups := []string{}
	switch s.Level {
	case ScopeCustomer:
		customer = s.CustomerID
	case ScopeGroup:
		// A GROUP scope always resolves to at least its anchor, so the array
		// is never empty here. ResolveGroupSubtree guarantees the anchor is
		// included; an empty array would compare false and deny every row,
		// which is the safe direction but hides a resolver bug, so it is
		// normalized away rather than relied upon.
		if len(s.GroupIDs) > 0 {
			groups = s.GroupIDs
		} else {
			groups = []string{s.GroupID}
		}
	}
	return []any{customer, groups}
}

// scopeRank orders scopes from widest to narrowest so merging several
// memberships in one tenant keeps the widest. A lower rank is wider.
//
// TENANT is widest because it names no restriction. SELF ranks narrowest so a
// hypothetical future merge can never widen into it.
var scopeRank = map[string]int{
	ScopeTenant:   0,
	ScopeCustomer: 1,
	ScopeGroup:    2,
	ScopeSelf:     3,
}

// WidestScope returns the wider of two scope levels. An unknown or empty
// level counts as TENANT so a corrupt stored value cannot deny the tenant
// admin; failing closed there would lock out the whole tenant.
func WidestScope(a, b string) string {
	ra, knownA := scopeRank[a]
	rb, knownB := scopeRank[b]
	switch {
	case !knownA:
		return ScopeTenant
	case !knownB:
		return ScopeTenant
	case ra <= rb:
		return a
	default:
		return b
	}
}

// IsValidScope reports whether level may be assigned to a membership.
func IsValidScope(level string) bool {
	_, ok := scopeRank[level]
	return ok
}

// ValidateScope rejects scopes that cannot be honored. SELF is refused
// outright: ADR-0022 lists it, but no resource in the current schema is owned
// by a user, so accepting it would promise a narrowing the data layer cannot
// express. Rejecting is honest; silently treating it as TENANT would leak.
func ValidateScope(level, scopeID string) error {
	if !IsValidScope(level) {
		return fmt.Errorf("unsupported data scope %q", level)
	}
	if level == ScopeSelf {
		return fmt.Errorf(
			"data scope SELF is not supported: no resource is owned by a user")
	}
	switch level {
	case ScopeCustomer, ScopeGroup:
		if _, err := uuid.Parse(scopeID); err != nil {
			return fmt.Errorf("data scope %s requires a valid scope_id", level)
		}
	case ScopeTenant:
		if scopeID != "" {
			return fmt.Errorf("data scope TENANT must not carry a scope_id")
		}
	}
	return nil
}

// DataScopeFor resolves a stored tenant grant into an executable DataScope.
//
// It validates that the anchoring customer/group still exists and belongs to
// the same tenant, then expands a GROUP scope into its full subtree. An
// unknown or cross-tenant anchor is reported as an error rather than silently
// widened: a membership pointing at another tenant's group must never widen
// into that tenant, and must never quietly become tenant-wide either.
func (r *Resolver) DataScopeFor(ctx context.Context,
	tenantID string, grant TenantGrant) (DataScope, error) {
	if tenantID == "" {
		return DataScope{}, fmt.Errorf("resolve data scope: empty tenant")
	}
	if err := ValidateScope(grant.Scope, grant.ScopeID); err != nil {
		return DataScope{}, err
	}
	switch grant.Scope {
	case ScopeTenant:
		return UnrestrictedScope(tenantID), nil

	case ScopeCustomer:
		owned, err := r.customerInTenant(ctx, tenantID, grant.ScopeID)
		if err != nil {
			return DataScope{}, err
		}
		if !owned {
			return DataScope{}, fmt.Errorf(
				"data scope customer %s does not belong to tenant %s",
				grant.ScopeID, tenantID)
		}
		return DataScope{
			TenantID: tenantID, Level: ScopeCustomer, CustomerID: grant.ScopeID,
		}, nil

	case ScopeGroup:
		groups, err := r.groupSubtree(ctx, tenantID, grant.ScopeID)
		if err != nil {
			return DataScope{}, err
		}
		return DataScope{
			TenantID: tenantID,
			Level:    ScopeGroup,
			GroupID:  grant.ScopeID,
			GroupIDs: groups,
		}, nil

	default:
		// ValidateScope already rejected everything else, including SELF.
		return DataScope{}, fmt.Errorf("unreachable data scope %q", grant.Scope)
	}
}

// groupSubtree returns the anchor group and all of its descendants, scoped to
// the tenant.
//
// The walk is done in Go rather than with a recursive CTE because it runs
// once per request and the result is then bound as a single uuid[] for
// every downstream predicate. Only the tenant's own groups are loaded, so
// every collected id is tenant-verified by construction, and a cycle in
// parent_id (corrupt data) is bounded by the visited set.
func (r *Resolver) groupSubtree(ctx context.Context,
	tenantID, anchorID string) ([]string, error) {
	type node struct{ id string }
	rows, err := r.pool.Query(ctx,
		`SELECT g.id::text, COALESCE(g.parent_id::text, '')
		 FROM device_group g
		 WHERE g.tenant_id = $1::uuid`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("load device groups: %w", err)
	}
	defer rows.Close()

	childrenOf := map[string][]string{}
	known := map[string]node{}
	for rows.Next() {
		var n node
		var parent string
		if err := rows.Scan(&n.id, &parent); err != nil {
			return nil, err
		}
		known[n.id] = n
		childrenOf[parent] = append(childrenOf[parent], n.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := known[anchorID]; !ok {
		return nil, fmt.Errorf(
			"data scope group %s does not belong to tenant %s", anchorID, tenantID)
	}

	// Breadth-first from the anchor. Only whole-tenant groups were loaded, so
	// every collected id is already tenant-verified.
	out := make([]string, 0, 8)
	seen := map[string]bool{anchorID: true}
	queue := []string{anchorID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		out = append(out, current)
		for _, child := range childrenOf[current] {
			if seen[child] {
				continue
			}
			seen[child] = true
			queue = append(queue, child)
		}
	}
	return out, nil
}

// customerInTenant reports whether the customer exists and belongs to tenant.
func (r *Resolver) customerInTenant(ctx context.Context,
	tenantID, customerID string) (bool, error) {
	var owned bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM customer
		                WHERE id = $2::uuid AND tenant_id = $1::uuid)`,
		tenantID, customerID).Scan(&owned)
	if err != nil {
		return false, fmt.Errorf("verify data scope customer: %w", err)
	}
	return owned, nil
}
