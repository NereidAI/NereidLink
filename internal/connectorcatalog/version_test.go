// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package connectorcatalog

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.9", "1.10", -1},
		{"1.10", "1.9", 1},
		{"2.1", "2.1", 0},
		{"v1.2", "1.2", 0},
		{"1.2.1", "1.2", 1},
		{"1.2", "1.2.1", -1},
		{"2.0", "1.99", 1},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveVersionSelectsHighest(t *testing.T) {
	reg := newFakeRegistry()
	s := testService(reg)
	ctx := context.Background()
	register := func(v string) {
		if _, err := reg.Upsert(ctx, Registration{ConnectorType: "modbus", ConnectorTypeVersion: v,
			ConfigSchema: json.RawMessage(`{"type":"object"}`)}, "t1", "g1", SourceEdge); err != nil {
			t.Fatal(err)
		}
	}
	register("1.9")
	register("1.10")
	register("2.0")

	// Blank -> highest registered.
	got, err := s.ResolveVersion(ctx, "t1", "modbus", "")
	if err != nil || got != "2.0" {
		t.Fatalf("blank version must resolve to 2.0, got %q err=%v", got, err)
	}
	// Explicit version is passed through untouched.
	got, err = s.ResolveVersion(ctx, "t1", "modbus", "1.9")
	if err != nil || got != "1.9" {
		t.Fatalf("explicit version must be preserved, got %q err=%v", got, err)
	}
	// Unknown type with no versioned schema -> "" without error.
	got, err = s.ResolveVersion(ctx, "t1", "brand-new", "")
	if err != nil || got != "" {
		t.Fatalf("unknown type must resolve blank without error, got %q err=%v", got, err)
	}
}
