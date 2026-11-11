// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// principal_test.go covers the machine principal branch of HasPoint
// (ADR-0021): authorization comes exclusively from the token scopes, is
// confined to one tenant and can never reach service-account
// administration.

package rbac

import "testing"

// TestMachinePrincipalHasPoint verifies scope-based authorization for
// an OAuth2 service account.
func TestMachinePrincipalHasPoint(t *testing.T) {
	p := Principal{
		UserID:  "sa-1",
		Machine: true,
		Scopes:  []string{PointDeviceRead, PointAlarmAck},
		Tenants: map[string]TenantGrant{
			"tenant-a": {Scope: ScopeTenant, Roles: []string{RoleServiceAccount}},
		},
	}

	cases := []struct {
		name   string
		tenant string
		point  string
		want   bool
	}{
		{"granted point same tenant", "tenant-a", PointDeviceRead, true},
		{"ungranted point same tenant", "tenant-a", PointDeviceWrite, false},
		{"granted point other tenant", "tenant-b", PointDeviceRead, false},
		{"identity admin never grantable via wildcard", "tenant-a", PointServiceAccountWrite, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.HasPoint(tc.tenant, tc.point); got != tc.want {
				t.Fatalf("HasPoint(%s,%s) = %v, want %v",
					tc.tenant, tc.point, got, tc.want)
			}
		})
	}
}

// TestMachinePrincipalWildcard verifies '*' grants every ordinary point
// but not identity administration.
func TestMachinePrincipalWildcard(t *testing.T) {
	p := Principal{
		Machine: true,
		Scopes:  []string{PointAll},
		Tenants: map[string]TenantGrant{
			"t": {Scope: ScopeTenant, Roles: []string{RoleServiceAccount}},
		},
	}
	if !p.HasPoint("t", PointDeviceCommand) {
		t.Fatal("wildcard should grant device:command")
	}
	if p.HasPoint("t", PointServiceAccountWrite) {
		t.Fatal("wildcard must not grant service-account administration to a machine")
	}
}

// TestMachinePrincipalNoRolePoints verifies the SERVICE_ACCOUNT role
// label is not itself a source of points, even though it is stored in
// the grant for audit.
func TestMachinePrincipalNoRolePoints(t *testing.T) {
	p := Principal{
		Machine: true,
		Scopes:  nil,
		Tenants: map[string]TenantGrant{
			"t": {Scope: ScopeTenant, Roles: []string{RoleServiceAccount}},
		},
	}
	if p.HasPoint("t", PointDeviceRead) {
		t.Fatal("a machine principal with no scopes must hold no points")
	}
}

// TestAPIKeyPrincipalExplicitScopes verifies a human API-key principal is
// narrowed to its explicit point subset while retaining its tenant grant.
func TestAPIKeyPrincipalExplicitScopes(t *testing.T) {
	p := Principal{
		APIKeyID: "key-1",
		Scopes:   []string{PointDeviceRead},
		Tenants: map[string]TenantGrant{
			"t": {Scope: ScopeTenant, Roles: []string{RoleEngineer}},
		},
	}
	if !p.HasPoint("t", PointDeviceRead) {
		t.Fatal("explicit API-key scope should authorize device:read")
	}
	if p.HasPoint("t", PointDeviceWrite) {
		t.Fatal("role permission outside the key scope must not be inherited")
	}
	if p.HasPoint("other", PointDeviceRead) {
		t.Fatal("API-key scope must not cross tenants")
	}
}

// TestAPIKeyPrincipalInheritance verifies empty key scopes use live roles.
func TestAPIKeyPrincipalInheritance(t *testing.T) {
	p := Principal{
		APIKeyID: "key-1",
		Tenants: map[string]TenantGrant{
			"t": {Scope: ScopeTenant, Roles: []string{RoleReadOnly}},
		},
	}
	if !p.HasPoint("t", PointDeviceRead) || p.HasPoint("t", PointDeviceWrite) {
		t.Fatal("inheritance-mode API key must follow its owner's roles")
	}
}

// TestHumanPrincipalUnaffected verifies membership-role authorization is
// unchanged for a non-machine principal.
func TestHumanPrincipalUnaffected(t *testing.T) {
	p := Principal{
		Tenants: map[string]TenantGrant{
			"t": {Scope: ScopeTenant, Roles: []string{RoleReadOnly}},
		},
	}
	if !p.HasPoint("t", PointDeviceRead) {
		t.Fatal("READ_ONLY should hold device:read")
	}
	if p.HasPoint("t", PointDeviceWrite) {
		t.Fatal("READ_ONLY must not hold device:write")
	}
}
