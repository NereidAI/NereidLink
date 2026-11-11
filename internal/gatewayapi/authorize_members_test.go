// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

func memberPrincipal(platform bool, grants map[string][]string) rbac.Principal {
	p := rbac.Principal{
		PlatformAdmin: platform,
		Tenants:       map[string]rbac.TenantGrant{},
	}
	for tenantID, roles := range grants {
		p.Tenants[tenantID] = rbac.TenantGrant{
			Scope: rbac.ScopeTenant,
			Roles: roles,
		}
	}
	return p
}

func TestEnforceMemberRoutes(t *testing.T) {
	const t1 = "11111111-1111-1111-1111-111111111111"
	const t2 = "22222222-2222-2222-2222-222222222222"

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/tenants/" + t1 + "/members"},
		{http.MethodPost, "/v1/tenants/" + t1 + "/members"},
		{http.MethodPost, "/v1/tenants/" + t1 + "/members/abc/roles"},
		{http.MethodPost, "/v1/tenants/" + t1 + "/members/abc/password"},
		{http.MethodDelete, "/v1/tenants/" + t1 + "/members/abc"},
	}

	tests := []struct {
		name      string
		principal rbac.Principal
		want      bool
	}{
		{"platform admin over any tenant",
			memberPrincipal(true, nil), true},
		{"tenant admin in own tenant",
			memberPrincipal(false, map[string][]string{t1: {rbac.RoleTenantAdmin}}), true},
		{"tenant admin reaching another tenant",
			memberPrincipal(false, map[string][]string{t2: {rbac.RoleTenantAdmin}}), false},
		{"engineer in own tenant",
			memberPrincipal(false, map[string][]string{t1: {rbac.RoleEngineer}}), false},
		{"read-only in own tenant",
			memberPrincipal(false, map[string][]string{t1: {rbac.RoleReadOnly}}), false},
		{"no membership at all",
			memberPrincipal(false, nil), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, rt := range routes {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(rt.method, rt.path, nil)
				if got := enforceRoute(rec, req, &tt.principal); got != tt.want {
					t.Errorf("%s %s: allowed=%v want %v (status %d)",
						rt.method, rt.path, got, tt.want, rec.Code)
				}
			}
		})
	}
}

func TestEnforceMemberDelegationDoesNotWidenOtherPlatformRoutes(t *testing.T) {
	// A TENANT_ADMIN must still be refused genuine cross-tenant platform
	// administration (tenant creation/listing, audit, licenses).
	const t1 = "11111111-1111-1111-1111-111111111111"
	p := memberPrincipal(false, map[string][]string{t1: {rbac.RoleTenantAdmin}})
	for _, path := range []string{"/v1/tenants", "/v1/audit/events", "/v1/platform/license"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if enforceRoute(rec, req, &p) {
			t.Errorf("tenant admin allowed on platform route %s", path)
		}
	}
}
