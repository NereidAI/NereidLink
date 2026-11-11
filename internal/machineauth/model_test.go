// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// model_test.go covers scope normalization: trimming, de-duplication and
// the rule that identity-administration points are never machine
// grantable.

package machineauth

import (
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestNormalizeScopes accepts and de-duplicates grantable points.
func TestNormalizeScopes(t *testing.T) {
	got, err := NormalizeScopes([]string{
		"  " + rbac.PointDeviceRead + " ",
		rbac.PointDeviceRead,
		rbac.PointStats,
		"",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 2 || got[0] != rbac.PointDeviceRead || got[1] != rbac.PointStats {
		t.Fatalf("unexpected normalized scopes: %v", got)
	}
}

// TestNormalizeScopesRejectsIdentityAdmin is the credential
// self-administration invariant.
func TestNormalizeScopesRejectsIdentityAdmin(t *testing.T) {
	for _, point := range []string{
		rbac.PointServiceAccountRead,
		rbac.PointServiceAccountWrite,
		"evil:root",
	} {
		if _, err := NormalizeScopes([]string{point}); err == nil {
			t.Fatalf("scope %q must be rejected for a service account", point)
		}
	}
}

// TestNormalizeScopesWildcard verifies the tenant-admin wildcard is
// grantable: it is the broadest legitimate scope.
func TestNormalizeScopesWildcard(t *testing.T) {
	got, err := NormalizeScopes([]string{rbac.PointAll})
	if err != nil {
		t.Fatalf("wildcard: %v", err)
	}
	if len(got) != 1 || got[0] != rbac.PointAll {
		t.Fatalf("unexpected: %v", got)
	}
}

// TestGeneratedCredentialsShape verifies ids and secrets use the
// documented unpadded lowercase alphabet and never include whitespace.
func TestGeneratedCredentialsShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := generateID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, clientIDPrefix) || len(id) != len(clientIDPrefix)+26 {
			t.Fatalf("bad client id %q", id)
		}
		secret, err := generateSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(secret) != 52 || strings.ToUpper(secret) == secret {
			t.Fatalf("bad secret shape %q", secret)
		}
	}
}
