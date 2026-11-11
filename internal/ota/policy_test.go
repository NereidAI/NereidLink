// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy_test.go covers the pure batch/abort policy math (ADR-0006d) without a
// database.
package ota

import "testing"

func ptr(v int) *int { return &v }

func TestBuildBatchesPercent(t *testing.T) {
	p := RolloutPolicy{BatchPercent: ptr(25)}
	plan := buildBatches(8, p, true)
	want := []int{1, 1, 2, 2, 3, 3, 4, 4}
	for i := range want {
		if plan[i] != want[i] {
			t.Fatalf("25%% of 8 plan=%v want %v", plan, want)
		}
	}
}

func TestBuildBatchesSize(t *testing.T) {
	p := RolloutPolicy{BatchSize: ptr(3)}
	plan := buildBatches(7, p, true)
	want := []int{1, 1, 1, 2, 2, 2, 3}
	for i := range want {
		if plan[i] != want[i] {
			t.Fatalf("size 3 of 7 plan=%v want %v", plan, want)
		}
	}
}

func TestBuildBatchesLegacySingle(t *testing.T) {
	plan := buildBatches(5, RolloutPolicy{}, false)
	for i, n := range plan {
		if n != 0 {
			t.Fatalf("legacy plan[%d]=%d want 0 (single batch): %v", i, n, plan)
		}
	}
}

func TestCumulativeOpenedRounding(t *testing.T) {
	// 10% of 3 devices must still open at least one device per batch.
	p := RolloutPolicy{BatchPercent: ptr(10)}
	if got := cumulativeOpened(3, p, 1); got != 1 {
		t.Fatalf("first batch cum=%d want 1", got)
	}
	if got := cumulativeOpened(3, p, 9); got != 3 {
		t.Fatalf("late batch cum=%d want 3", got)
	}
}

func TestAbortReason(t *testing.T) {
	pct := RolloutPolicy{FailureAbortPercent: ptr(20), MaxConsecutiveFailures: ptr(5)}
	// Exactly 20% trips the threshold.
	if abortReason(pct, BatchSettle{Succeeded: 8, Failed: 2}) == "" {
		t.Fatal("2/10 failures must trip the 20% threshold")
	}
	if r := abortReason(pct, BatchSettle{Succeeded: 100, Failed: 2}); r != "" {
		t.Fatalf("2/102 failures must not trip: %q", r)
	}
	// Five failures in a row trip the consecutive threshold while the
	// overall rate (5/30 = 16%) stays under the 20% rate tripwire.
	if abortReason(pct, BatchSettle{Succeeded: 25, Failed: 5, ConsecutiveFailures: 5}) == "" {
		t.Fatal("5 trailing failures must trip the consecutive threshold")
	}
	// Open devices with no failures yet do not abort.
	if r := abortReason(pct, BatchSettle{Open: 4, Succeeded: 1}); r != "" {
		t.Fatalf("no settled failures must not abort: %q", r)
	}
	// Legacy nil thresholds never auto-abort.
	if r := abortReason(RolloutPolicy{}, BatchSettle{Failed: 50}); r != "" {
		t.Fatalf("legacy task must never auto-abort: %q", r)
	}
}
