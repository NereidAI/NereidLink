// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sketch_test.go pins the accounting invariants of the bounded
// heavy-hitter structures: memory stays flat under unbounded
// cardinality, the Space-Saving error bound holds, decay ages out old
// contributions, and Count-Min never under-estimates.

package accounting

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

// TestSketchStaysBounded verifies a million distinct values never grow
// the structure past its capacity.
func TestSketchStaysBounded(t *testing.T) {
	s := NewSketch(64)
	for i := 0; i < 1_000_000; i++ {
		s.Observe(fmt.Sprintf("value-%d", i))
	}
	if got := s.Tracked(); got > 64 {
		t.Fatalf("tracked %d candidates, capacity is 64", got)
	}
	if got := s.Samples(); got != 1_000_000 {
		t.Fatalf("expected 1000000 samples, got %d", got)
	}
	// A candidate's reported count must never claim more than the total
	// number of observations.
	for _, c := range s.Top(64) {
		if c.Count > 1_000_000 {
			t.Fatalf("candidate %q count %d exceeds total samples", c.Value, c.Count)
		}
	}
}

// TestSketchFindsFrequentValues verifies the heavy hitters survive.
func TestSketchFindsFrequentValues(t *testing.T) {
	s := NewSketch(16)
	for i := 0; i < 1000; i++ {
		s.Observe("hot-tenant")
	}
	for i := 0; i < 10; i++ {
		s.Observe("warm-tenant")
	}
	for i := 0; i < 5000; i++ {
		s.Observe(fmt.Sprintf("noise-%d", i))
	}
	top := s.Top(3)
	if len(top) == 0 || top[0].Value != "hot-tenant" {
		t.Fatalf("expected hot-tenant first, got %+v", top)
	}
	if top[0].MinCount() < 1000-64 {
		t.Fatalf("hot-tenant lower bound %d too low", top[0].MinCount())
	}
}

// TestSketchErrorBoundHolds verifies Count-Error never exceeds the true
// count for a tracked candidate.
func TestSketchErrorBoundHolds(t *testing.T) {
	s := NewSketch(8)
	truth := map[string]uint64{}
	for i := 0; i < 20000; i++ {
		value := fmt.Sprintf("v%d", i%500)
		truth[value]++
		s.Observe(value)
	}
	for _, c := range s.Top(8) {
		if c.MinCount() > truth[c.Value] {
			t.Fatalf("candidate %q lower bound %d exceeds true count %d",
				c.Value, c.MinCount(), truth[c.Value])
		}
	}
}

// TestSketchResetKeepsWindowsDisjoint is the regression guard for the
// windowing contract the store depends on: consecutive reports cover
// consecutive disjoint intervals, so summing them must reproduce the true
// total. A decaying counter would report the same traffic again in every
// later window and inflate the sum, which is exactly the bug this test
// was written for.
func TestSketchResetKeepsWindowsDisjoint(t *testing.T) {
	s := NewSketch(8)
	for i := 0; i < 1000; i++ {
		s.Observe("old")
	}
	if _, ok := s.Lookup("old"); !ok {
		t.Fatal("old should hold a slot before reset")
	}
	s.Reset()
	if got := s.Samples(); got != 0 {
		t.Fatalf("expected samples cleared after reset, got %d", got)
	}
	if _, ok := s.Lookup("old"); ok {
		t.Fatal("old candidate should be gone after reset")
	}
	if got := s.Tracked(); got != 0 {
		t.Fatalf("expected no tracked candidates after reset, got %d", got)
	}
}

// TestSketchWindowsSumToTotal is the arithmetic half of the same
// guarantee: over several windows the per-window reports must add up to
// the number of frames actually observed, neither more nor less.
func TestSketchWindowsSumToTotal(t *testing.T) {
	const windows = 5
	const perWindow = 400
	s := NewSketch(8)
	var summed uint64
	for w := 0; w < windows; w++ {
		for i := 0; i < perWindow; i++ {
			s.Observe("device-a")
		}
		candidates := s.Top(1)
		if len(candidates) != 1 {
			t.Fatalf("window %d: expected 1 candidate, got %d", w, len(candidates))
		}
		summed += candidates[0].Count
		s.Reset()
	}
	if want := uint64(windows * perWindow); summed != want {
		t.Fatalf("summed window counts = %d, want %d", summed, want)
	}
}

// TestSketchConcurrentObserve exercises the hot-path concurrency guard.
func TestSketchConcurrentObserve(t *testing.T) {
	s := NewSketch(128)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				s.Observe(fmt.Sprintf("t%d-v%d", w, i%20))
			}
		}(w)
	}
	wg.Wait()
	if got := s.Samples(); got != 8*5000 {
		t.Fatalf("expected 40000 samples, got %d", got)
	}
	if got := s.Tracked(); got > 128 {
		t.Fatalf("tracked %d exceeds capacity", got)
	}
}

// TestSketchSaturatesByteTotal verifies a hot value's byte total clamps
// instead of wrapping negative. A negative total would surface as negative
// traffic in the accounting API, which is worse than reporting "very
// large".
func TestSketchSaturatesByteTotal(t *testing.T) {
	s := NewSketch(4)
	// Seed a candidate close to the signed maximum, then push it over the
	// edge with a normal frame size.
	s.ObserveWeighted("hot", math.MaxInt64-1)
	s.ObserveWeighted("hot", 1000)
	got, ok := s.Lookup("hot")
	if !ok {
		t.Fatal("hot value should hold a slot")
	}
	if got.Bytes <= 0 {
		t.Fatalf("byte total wrapped to %d", got.Bytes)
	}
	if got.Bytes != math.MaxInt64 {
		t.Fatalf("byte total = %d, want clamp at %d", got.Bytes, int64(math.MaxInt64))
	}
}

// TestSaturateAddClampsInBothDirections covers the helper directly,
// including the negative operand path used by the Space-Saving admission
// logic.
func TestSaturateAddClampsInBothDirections(t *testing.T) {
	cases := []struct {
		a, b, want int64
	}{
		{1, 2, 3},
		{0, 5, 5},
		{math.MaxInt64, 1, math.MaxInt64},
		{math.MaxInt64 - 5, 100, math.MaxInt64},
		{math.MinInt64, -1, math.MinInt64},
	}
	for _, tc := range cases {
		if got := saturateAdd(tc.a, tc.b); got != tc.want {
			t.Fatalf("saturateAdd(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
