// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

func TestParseLinkGroups(t *testing.T) {
	t1 := "6f1e2b7c-9a4d-4c3e-8b21-0d5f7a9c1e42"
	t2 := "a2c4e6f8-1b3d-4e5f-9a7b-2c4d6e8f0a1b"

	tests := []struct {
		name   string
		groups []string
		want   []tenantRole
	}{
		{
			name: "link entries parsed",
			groups: []string{
				"/nereidlink/" + t1 + "/admins",
				"/nereidlink/" + t2 + "/engineers",
			},
			want: []tenantRole{
				{TenantID: t1, RoleKey: "admins"},
				{TenantID: t2, RoleKey: "engineers"},
			},
		},
		{
			name: "other products and engine defaults ignored",
			groups: []string{
				"/nereidforge/" + t1 + "/admin",
				"/nereidhorizon/" + t1 + "/read_only",
				"/user",
				"/realm-management/manage-users",
			},
			want: nil,
		},
		{
			name: "malformed paths ignored",
			groups: []string{
				"/nereidlink/" + t1,
				"/nereidlink/" + t1 + "/admins/extra",
				"/nereidlink//admins",
				"/nereidlink/" + t1 + "/",
				"nereidlink/" + t1 + "/admins",
			},
			want: nil,
		},
		{
			name: "non-uuid tenant ignored",
			groups: []string{
				"/nereidlink/default/admins",
				"/nereidlink/12345/read_only",
			},
			want: nil,
		},
		{
			name:   "nil and empty",
			groups: nil,
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLinkGroups(tt.groups)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d assignments, want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("assignment %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMapGroupRoleKey(t *testing.T) {
	s := &Service{cfg: Config{GroupRole: map[string]string{
		"admins":    rbac.RoleTenantAdmin,
		"engineers": rbac.RoleEngineer,
		"read_only": rbac.RoleReadOnly,
	}}}
	if got := s.mapGroupRoleKey("admins"); got != rbac.RoleTenantAdmin {
		t.Errorf("admins -> %q, want TENANT_ADMIN", got)
	}
	if got := s.mapGroupRoleKey("engineers"); got != rbac.RoleEngineer {
		t.Errorf("engineers -> %q, want ENGINEER", got)
	}
	if got := s.mapGroupRoleKey("read_only"); got != rbac.RoleReadOnly {
		t.Errorf("read_only -> %q, want READ_ONLY", got)
	}
	if got := s.mapGroupRoleKey("owner"); got != "" {
		t.Errorf("unknown key -> %q, want empty", got)
	}
}

func TestMapRoleFallback(t *testing.T) {
	s := &Service{cfg: Config{
		RoleMap:     map[string]string{"admin": rbac.RoleTenantAdmin},
		DefaultRole: rbac.RoleReadOnly,
	}}
	if got := s.mapRole([]string{"user", "admin"}); got != rbac.RoleTenantAdmin {
		t.Errorf("mapped realm role -> %q, want TENANT_ADMIN", got)
	}
	if got := s.mapRole([]string{"user"}); got != rbac.RoleReadOnly {
		t.Errorf("unmapped realm role -> %q, want READ_ONLY default", got)
	}
	s.cfg.DefaultRole = "BOGUS"
	if got := s.mapRole([]string{"user"}); got != "" {
		t.Errorf("invalid default role -> %q, want empty", got)
	}
}
