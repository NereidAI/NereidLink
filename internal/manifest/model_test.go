// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"
	"time"
)

func TestDiff(t *testing.T) {
	desired := []string{"a", "b", "c", "b", ""}
	reported := []string{"b", "c", "d", "d"}

	got := Diff(desired, reported)

	if len(got.InSync) != 2 || got.InSync[0] != "b" || got.InSync[1] != "c" {
		t.Fatalf("unexpected in_sync: %v", got.InSync)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "a" {
		t.Fatalf("unexpected missing: %v", got.Missing)
	}
	if len(got.Unregistered) != 1 || got.Unregistered[0] != "d" {
		t.Fatalf("unexpected unregistered: %v", got.Unregistered)
	}
	if !got.Drift() {
		t.Fatal("expected drift")
	}
}

func TestDiffNoDrift(t *testing.T) {
	got := Diff([]string{"x", "y"}, []string{"y", "x"})
	if got.Drift() {
		t.Fatalf("did not expect drift: %+v", got)
	}
	if len(got.InSync) != 2 {
		t.Fatalf("expected both in sync, got %v", got.InSync)
	}
	if got.Missing != nil || got.Unregistered != nil {
		t.Fatalf("expected nil empty sets, got %v %v", got.Missing, got.Unregistered)
	}
}

func TestDiffEmpty(t *testing.T) {
	got := Diff(nil, nil)
	if got.Drift() {
		t.Fatal("empty manifest must not drift")
	}
}

func TestLeaseTrackerDeriveOnce(t *testing.T) {
	tr := newLeaseTracker()
	now := time.Now().UTC()
	key := "tenant.gateway"

	tr.observe(key, now)
	if tr.shouldDerive(key, now.Add(10*time.Second), time.Minute) {
		t.Fatal("fresh lease must not derive")
	}
	if !tr.shouldDerive(key, now.Add(2*time.Minute), time.Minute) {
		t.Fatal("stale lease must derive")
	}
	if tr.shouldDerive(key, now.Add(3*time.Minute), time.Minute) {
		t.Fatal("stale lease must derive only once")
	}

	// A fresh sighting re-arms derivation.
	tr.observe(key, now.Add(5*time.Minute))
	if tr.shouldDerive(key, now.Add(5*time.Minute), time.Minute) {
		t.Fatal("renewed lease must not derive")
	}
	if !tr.shouldDerive(key, now.Add(7*time.Minute), time.Minute) {
		t.Fatal("re-expired lease must derive again")
	}
}

func TestSplitLeaseKey(t *testing.T) {
	tenant, gateway, ok := splitLeaseKey("tenant-uuid.gateway-uuid")
	if !ok || tenant != "tenant-uuid" || gateway != "gateway-uuid" {
		t.Fatalf("unexpected split: %q %q %v", tenant, gateway, ok)
	}
	if _, _, ok := splitLeaseKey("nodot"); ok {
		t.Fatal("key without dot must not split")
	}
	if _, _, ok := splitLeaseKey(".gateway"); ok {
		t.Fatal("empty tenant must not split")
	}
}
