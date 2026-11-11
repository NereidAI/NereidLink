// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package userapikey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

type stubPermissionReader struct {
	points []string
}

func (s stubPermissionReader) PermissionPointsForUser(context.Context, string, string) ([]string, error) {
	return s.points, nil
}

func TestGenerateKeyFormat(t *testing.T) {
	first, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate first key: %v", err)
	}
	second, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	for _, key := range []string{first, second} {
		if !strings.HasPrefix(key, keyPrefix) {
			t.Fatalf("key %q lacks prefix %q", key, keyPrefix)
		}
		if len(key) != len(keyPrefix)+keySuffixLength {
			t.Fatalf("key length = %d, want %d", len(key), len(keyPrefix)+keySuffixLength)
		}
		if strings.ToLower(key) != key {
			t.Fatalf("key %q must be lowercase", key)
		}
		if _, err := LookupPrefix(key); err != nil {
			t.Fatalf("valid key rejected: %v", err)
		}
	}
	if first == second {
		t.Fatal("generated keys must not repeat")
	}
}

func TestLookupPrefixRejectsMalformedKeys(t *testing.T) {
	valid, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"",
		"nlk_short",
		"xyz_" + strings.TrimPrefix(valid, keyPrefix),
		strings.ToUpper(valid),
		keyPrefix + strings.Repeat("0", keySuffixLength),
		keyPrefix + strings.Repeat("a", keySuffixLength-1),
	}
	for _, raw := range cases {
		if _, err := LookupPrefix(raw); err == nil {
			t.Fatalf("malformed key accepted: %q", raw)
		}
	}
	prefix, err := LookupPrefix(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefix) != storedPrefixLength || !strings.HasPrefix(prefix, keyPrefix) {
		t.Fatalf("unexpected stored prefix %q", prefix)
	}
}

func TestEnsureScopeSubset(t *testing.T) {
	ctx := context.Background()
	admin := New(nil)
	admin.SetPermissionReader(stubPermissionReader{points: []string{rbac.PointAll}})
	if err := admin.ensureScopeSubset(ctx, "user", "tenant",
		[]string{rbac.PointExternalAccessWrite}); err != nil {
		t.Fatalf("tenant-admin wildcard should cover a known point: %v", err)
	}

	limited := New(nil)
	limited.SetPermissionReader(stubPermissionReader{
		points: []string{rbac.PointDeviceRead},
	})
	if err := limited.ensureScopeSubset(ctx, "user", "tenant",
		[]string{rbac.PointDeviceWrite}); !errors.Is(err, ErrScopeNotAllowed) {
		t.Fatalf("expected scope rejection, got %v", err)
	}
}

func TestNormalizeScopes(t *testing.T) {
	got, err := NormalizeScopes([]string{
		" " + rbac.PointDeviceRead + " ",
		rbac.PointDeviceRead,
		"",
		rbac.PointAlarmRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{rbac.PointDeviceRead, rbac.PointAlarmRead}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("normalized scopes = %v, want %v", got, want)
	}
	if _, err := NormalizeScopes([]string{"root:all"}); err == nil {
		t.Fatal("unknown scope must be rejected")
	}
	empty, err := NormalizeScopes(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty scopes should inherit permissions, got %v %v", empty, err)
	}
}
