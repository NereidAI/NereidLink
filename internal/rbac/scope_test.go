// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// scope_test.go covers the executable half of ADR-0022: the SQL narrowing a
// data scope produces, and the merge that picks the effective scope when one
// membership contributes several roles.
//
// The predicate tests are the important ones. A narrowing clause that is
// accidentally always true is silent in every other sense — the queries still
// run, the results still look plausible, and the leak only shows up as "a
// customer user can see the whole tenant" in production. They therefore assert
// on the generated SQL, not merely on the presence of a clause.

package rbac

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// TestUnrestrictedScopeGeneratesNoPredicate verifies that a tenant-wide scope
// appends neither SQL nor arguments, so the internal callers (rule engine,
// ingress, background jobs) keep full-tenant access by doing nothing.
func TestUnrestrictedScopeGeneratesNoPredicate(t *testing.T) {
	scope := UnrestrictedScope("tenant-1")

	if scope.Restricted() {
		t.Fatal("tenant scope must not be restricted")
	}
	clause, args := scope.DevicePredicate("", 3)
	if clause != "" {
		t.Errorf("unrestricted scope must not narrow, got %q", clause)
	}
	if args != nil {
		t.Errorf("unrestricted scope must not append args, got %v", args)
	}
	clause, args = scope.DeviceSubqueryPredicate("a.device_id", 3)
	if clause != "" || args != nil {
		t.Errorf("unrestricted subquery must not narrow, got %q %v", clause, args)
	}
}

// TestMissingScopeIsUnrestricted verifies the zero-value behavior: a context
// without a scope is treated as system level. Failing closed here would lock
// out every internal caller the moment a context value were dropped.
func TestMissingScopeIsUnrestricted(t *testing.T) {
	scope := ScopeFromContext(context.Background())
	if scope.Restricted() {
		t.Error("a context without a scope must be unrestricted")
	}
	if got := scope.Scope(); got != ScopeTenant {
		t.Errorf("missing scope level = %q, want %q", got, ScopeTenant)
	}
}

// TestDevicePredicateDisablesUnusedArm is the regression test for the widening
// bug this clause was first written with.
//
// A leading guard of the form `($1 = ” OR <match>)` makes the WHOLE
// predicate true whenever the customer argument is empty, which is exactly the
// GROUP case. A group-scoped member would then see the entire tenant while the
// code and the audit trail both claimed a narrowing. NULLIF yields NULL, which
// no row satisfies, so the unused arm stays inert.
func TestDevicePredicateDisablesUnusedArm(t *testing.T) {
	customer := DataScope{TenantID: "t1", Level: ScopeCustomer, CustomerID: "cust-1"}
	group := DataScope{
		TenantID: "t1", Level: ScopeGroup, GroupID: "g1",
		GroupIDs: []string{"g1", "g2"},
	}

	groupClause, groupArgs := group.DevicePredicate("d", 2)
	if strings.Contains(groupClause, "= ''") {
		t.Errorf("group scope clause must not contain an always-true guard: %q",
			groupClause)
	}
	if !strings.Contains(groupClause, "NULLIF($3,'')") {
		t.Errorf("customer arm must be neutralized with NULLIF, got %q", groupClause)
	}
	// The customer argument is empty for a group scope, so that arm cannot
	// match anything: NULLIF('','') is NULL and no customer_id equals NULL.
	if groupArgs[0] != "" {
		t.Errorf("group scope customer arg = %v, want empty", groupArgs[0])
	}

	customerClause, customerArgs := customer.DevicePredicate("d", 2)
	if strings.Contains(customerClause, "= ''") {
		t.Errorf("customer scope clause must not contain an always-true guard: %q",
			customerClause)
	}
	if customerArgs[1] == nil {
		t.Error("group arm must be bound to an array, not nil")
	}
	if arr, ok := customerArgs[1].([]string); !ok || len(arr) != 0 {
		t.Errorf("customer scope group arg = %v, want empty slice", customerArgs[1])
	}
}

// TestDevicePredicatePlaceholderNumbering verifies the generated clause uses
// the host query's own argument offsets. Off-by-one numbering here produces a
// runtime 42P08 or, worse, silently binds the tenant id into a uuid cast.
func TestDevicePredicatePlaceholderNumbering(t *testing.T) {
	scope := DataScope{
		TenantID: "t1", Level: ScopeCustomer, CustomerID: "cust-1",
	}
	for _, existing := range []int{0, 2, 9, 11} {
		clause, args := scope.DevicePredicate("d", existing)
		for _, want := range []string{
			"$" + strconv.Itoa(existing+1), "$" + strconv.Itoa(existing+2),
		} {
			if !strings.Contains(clause, want) {
				t.Errorf("existing=%d: clause missing %s: %s",
					existing, want, clause)
			}
		}
		// The device-table variant appends exactly the two ownership
		// parameters; both must be present because the clause references
		// both placeholders even when only one arm carries a value.
		if len(args) != 2 {
			t.Errorf("existing=%d: got %d args, want 2", existing, len(args))
		}
	}
}

// TestDevicePredicateQualifiesAlias verifies the ownership columns are
// qualified, so the clause cannot bind to a joined device table by accident.
func TestDevicePredicateQualifiesAlias(t *testing.T) {
	scope := DataScope{
		TenantID: "t1", Level: ScopeCustomer, CustomerID: "cust-1",
	}
	clause, _ := scope.DevicePredicate("d", 2)
	if !strings.Contains(clause, "d.customer_id") {
		t.Errorf("alias not applied to customer_id: %q", clause)
	}
	if !strings.Contains(clause, "d.group_id") {
		t.Errorf("alias not applied to group_id: %q", clause)
	}

	bare, _ := scope.DevicePredicate("", 2)
	if strings.Contains(bare, ".customer_id") {
		t.Errorf("empty alias must not produce a qualified column: %q", bare)
	}
}

// TestDeviceSubqueryPredicateRepeatsTenant verifies the membership subquery
// carries its own tenant predicate, so a crafted device id cannot reach a
// device in another tenant even if the outer table were mis-filtered.
func TestDeviceSubqueryPredicateRepeatsTenant(t *testing.T) {
	scope := DataScope{
		TenantID: "tenant-a", Level: ScopeCustomer, CustomerID: "cust-1",
	}
	clause, args := scope.DeviceSubqueryPredicate("a.device_id", 3)

	if !strings.Contains(clause, "tenant_id = $6") {
		t.Errorf("subquery must bind the tenant id at the trailing offset: %q", clause)
	}
	if !strings.Contains(clause, "a.device_id") {
		t.Errorf("subquery must test the supplied device id column: %q", clause)
	}
	if args[2] != "tenant-a" {
		t.Errorf("tenant arg = %v, want tenant-a", args[2])
	}
	if len(args) != 3 {
		t.Errorf("subquery variant got %d args, want 3", len(args))
	}
	if strings.Contains(clause, "= ''") {
		t.Errorf("subquery clause must not contain an always-true guard: %q", clause)
	}
}

// TestScopeFromContextRoundTrip verifies the context carrier preserves a
// narrowed scope.
func TestScopeFromContextRoundTrip(t *testing.T) {
	want := DataScope{
		TenantID: "t1", Level: ScopeGroup, GroupID: "g1",
		GroupIDs: []string{"g1", "g2"},
	}
	ctx := ContextWithScope(context.Background(), want)
	got := ScopeFromContext(ctx)

	if !got.Restricted() {
		t.Fatal("scope carried on ctx must be restricted")
	}
	if got.Scope() != ScopeGroup || got.GroupID != "g1" {
		t.Errorf("scope = %q/%q, want GROUP/g1", got.Scope(), got.GroupID)
	}
	if len(got.GroupIDs) != 2 {
		t.Errorf("group subtree = %v, want 2 entries", got.GroupIDs)
	}
	// ScopeIDs must copy rather than alias, so a caller mutating the returned
	// slice cannot corrupt the request scope for later queries.
	ids := got.ScopeIDs()
	if len(ids) != 2 {
		t.Fatalf("ScopeIDs = %v, want 2 entries", ids)
	}
	ids[0] = "mutated"
	if got.GroupIDs[0] != "g1" {
		t.Error("ScopeIDs must return a copy of the subtree")
	}
}

// TestWidestScope covers the merge used when one membership contributes
// several roles, and the fail-open behavior for an unknown stored value.
func TestWidestScope(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{ScopeTenant, ScopeCustomer, ScopeTenant},
		{ScopeCustomer, ScopeTenant, ScopeTenant},
		{ScopeCustomer, ScopeGroup, ScopeCustomer},
		{ScopeGroup, ScopeCustomer, ScopeCustomer},
		{ScopeGroup, ScopeGroup, ScopeGroup},
		// An empty or unknown value is treated as tenant-wide: a corrupt
		// stored scope must not be able to deny the tenant admin access.
		{"", ScopeCustomer, ScopeTenant},
		{ScopeCustomer, "", ScopeTenant},
		{"BOGUS", ScopeGroup, ScopeTenant},
	}
	for _, tc := range cases {
		if got := WidestScope(tc.a, tc.b); got != tc.want {
			t.Errorf("WidestScope(%q, %q) = %q, want %q",
				tc.a, tc.b, got, tc.want)
		}
	}
}

// TestValidateScope covers the assignment-side rules, most importantly the
// refusal of SELF.
func TestValidateScope(t *testing.T) {
	cases := []struct {
		name    string
		level   string
		scopeID string
		wantErr bool
	}{
		{"tenant without anchor", ScopeTenant, "", false},
		{"customer with anchor", ScopeCustomer,
			"1f0d2f4e-6a1b-4c2e-9f3a-7d5e8c9b0a11", false},
		{"group with anchor", ScopeGroup,
			"1f0d2f4e-6a1b-4c2e-9f3a-7d5e8c9b0a11", false},
		{"customer without anchor", ScopeCustomer, "", true},
		{"group without anchor", ScopeGroup, "", true},
		{"customer with malformed anchor", ScopeCustomer, "not-a-uuid", true},
		// A leftover anchor on a tenant-wide scope is rejected: it would be
		// silently cleared by the write path and the caller would believe the
		// scope is still narrowed.
		{"tenant with anchor", ScopeTenant,
			"1f0d2f4e-6a1b-4c2e-9f3a-7d5e8c9b0a11", true},
		// SELF is named in ADR-0022 but no resource is owned by a user, so
		// it cannot be honored.
		{"self", ScopeSelf, "", true},
		{"unknown level", "GALAXY", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateScope(tc.level, tc.scopeID)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateScope(%q, %q) = nil, want error",
					tc.level, tc.scopeID)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateScope(%q, %q) = %v, want nil",
					tc.level, tc.scopeID, err)
			}
		})
	}
}
