// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"net/http"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/rbac"
)

func TestStateStoreSingleUseAndTTL(t *testing.T) {
	store := newStateStore(time.Minute)
	if err := store.put("s1", loginState{nonce: "n", expiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if store.len() != 1 {
		t.Fatalf("len = %d, want 1", store.len())
	}
	if _, ok := store.take("s1"); !ok {
		t.Fatal("first take should succeed")
	}
	if _, ok := store.take("s1"); ok {
		t.Fatal("state must be single-use")
	}
	if _, ok := store.take("missing"); ok {
		t.Fatal("unknown state must fail")
	}

	if err := store.put("expired", loginState{expiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.take("expired"); ok {
		t.Fatal("expired state must fail")
	}
}

func TestClaimGroups(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"strings", []any{"a", "b", ""}, 2},
		{"typed", []string{"a"}, 1},
		{"single", "g1", 1},
		{"json array", `["x","y"]`, 2},
		{"nil", nil, 0},
		{"number", 42, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimGroups(tc.in); len(got) != tc.want {
				t.Fatalf("got %v, want %d groups", got, tc.want)
			}
		})
	}
}

func TestMapRole(t *testing.T) {
	s := &Service{cfg: Config{
		DefaultRole: rbac.RoleReadOnly,
		GroupRole: map[string]string{
			"admins":  rbac.RoleTenantAdmin,
			"ops":     rbac.RoleEngineer,
			"illegal": rbac.RolePlatformAdmin, // must be rejected from mapping
		},
	}}
	if got := s.mapRole([]string{"admins"}); got != rbac.RoleTenantAdmin {
		t.Fatalf("admins -> %q", got)
	}
	if got := s.mapRole([]string{"ops"}); got != rbac.RoleEngineer {
		t.Fatalf("ops -> %q", got)
	}
	if got := s.mapRole([]string{"unknown"}); got != rbac.RoleReadOnly {
		t.Fatalf("default -> %q", got)
	}
	if got := s.mapRole([]string{"illegal"}); got != rbac.RoleReadOnly {
		t.Fatalf("platform admin must not be group-assignable, got %q", got)
	}

	noDefault := &Service{cfg: Config{GroupRole: map[string]string{"a": rbac.RoleEngineer}}}
	if got := noDefault.mapRole([]string{"x"}); got != "" {
		t.Fatalf("unmapped with no default -> %q, want empty", got)
	}
}

func TestHTTPClientFromCAFile(t *testing.T) {
	if c, err := httpClientFromCAFile(""); err != nil || c != http.DefaultClient {
		t.Fatalf("empty path should return the default client, got err=%v", err)
	}
	if _, err := httpClientFromCAFile(t.TempDir() + "/missing.pem"); err == nil {
		t.Fatal("missing CA file must error")
	}
	if _, err := httpClientFromCAFile(writeTempPEM(t, []byte("not a certificate"))); err == nil {
		t.Fatal("a file without PEM certificates must error")
	}

	client, err := httpClientFromCAFile(writeTempPEM(t, testSelfSignedPEM(t)))
	if err != nil {
		t.Fatalf("valid PEM must load: %v", err)
	}
	roots := client.Transport.(*http.Transport).TLSClientConfig.RootCAs
	if !roots.AppendCertsFromPEM(testSelfSignedPEM(t)) {
		t.Fatal("pool should already contain the PEM and accept re-append")
	}
}
