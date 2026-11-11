// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestlimit

import (
	"testing"
	"time"
)

// TestBurstThenDeny confirms a device can spend its burst tokens up
// front and is then denied until tokens refill.
func TestBurstThenDeny(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(Config{Rate: 10, Burst: 10})

	for i := 0; i < 10; i++ {
		if !l.Allow("tenant.device", now) {
			t.Fatalf("frame %d should be admitted within burst", i)
		}
	}
	if l.Allow("tenant.device", now) {
		t.Fatal("11th frame must be denied with no elapsed time")
	}
}

// TestRefill verifies tokens accrue over wall time and the cap is not
// exceeded.
func TestRefill(t *testing.T) {
	base := time.Unix(0, 0)
	l := New(Config{Rate: 10, Burst: 10})

	for i := 0; i < 10; i++ {
		l.Allow("k", base)
	}
	// Half a second at 10/s refills 5 tokens.
	at := base.Add(500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		if !l.Allow("k", at) {
			t.Fatalf("refilled frame %d should pass", i)
		}
	}
	if l.Allow("k", at) {
		t.Fatal("6th frame must fail: only 5 tokens refilled")
	}

	// A long wait must not exceed the burst cap.
	later := at.Add(time.Minute)
	for i := 0; i < 10; i++ {
		if !l.Allow("k", later) {
			t.Fatalf("frame %d should pass at full burst", i)
		}
	}
}

// TestUnlimited confirms a zero rate admits everything.
func TestUnlimited(t *testing.T) {
	l := New(Config{Rate: 0})
	now := time.Unix(0, 0)
	for i := 0; i < 10000; i++ {
		if !l.Allow("k", now) {
			t.Fatal("zero rate must be unlimited")
		}
	}
	if l.Count() != 0 {
		t.Fatal("unlimited limiter must not store buckets")
	}
}

// TestSweep evicts only stale buckets.
func TestSweep(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(Config{Rate: 1, Burst: 1})
	l.Allow("old", now)
	l.Allow("fresh", now.Add(9*time.Minute))

	removed := l.Sweep(now.Add(10*time.Minute), time.Minute)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if l.Count() != 1 {
		t.Fatalf("count = %d, want 1", l.Count())
	}
}

// recordingSink captures flood lifecycle events.
type recordingSink struct {
	started   []FloodEvent
	recovered []FloodEvent
}

func (s *recordingSink) FloodStarted(ev FloodEvent)   { s.started = append(s.started, ev) }
func (s *recordingSink) FloodRecovered(ev FloodEvent) { s.recovered = append(s.recovered, ev) }

// denyN forces n denials at time now by exhausting a 1-token bucket.
func denyN(t *testing.T, l *Limiter, key string, now time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if l.Allow(key, now) {
			t.Fatalf("frame %d expected to be denied", i)
		}
	}
}

// TestFloodStartOnceAndRecover verifies the threshold opens exactly one
// flood and a calm recovery window auto-clears it once.
func TestFloodStartOnceAndRecover(t *testing.T) {
	base := time.Unix(0, 0)
	// Rate 1 burst 1: first frame admits, every same-instant frame denies.
	l := New(Config{Rate: 1, Burst: 1})
	sink := &recordingSink{}
	l.SetFloodSink(FloodDetector{
		Threshold: 5,
		Window:    10 * time.Second,
		Recover:   30 * time.Second,
	}, sink)

	if !l.Allow("t.d", base) {
		t.Fatal("first frame should admit")
	}
	denyN(t, l, "t.d", base, 4) // below threshold
	if len(sink.started) != 0 {
		t.Fatalf("started = %d, want 0 below threshold", len(sink.started))
	}
	denyN(t, l, "t.d", base, 6) // cross and keep flooding
	if len(sink.started) != 1 {
		t.Fatalf("started = %d, want exactly 1", len(sink.started))
	}
	if sink.started[0].Rejections != 5 {
		t.Fatalf("rejections = %d, want 5", sink.started[0].Rejections)
	}

	// Still flooding: recovery must not fire.
	if n := l.SweepFlood(base.Add(20*time.Second), time.Hour); n != 0 {
		t.Fatalf("recovered while flooding = %d, want 0", n)
	}

	// Calm for the recovery window: one recover event.
	if n := l.SweepFlood(base.Add(31*time.Second), time.Hour); n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}
	if len(sink.recovered) != 1 {
		t.Fatalf("recovered events = %d, want 1", len(sink.recovered))
	}
}

// TestFloodWindowExpires ensures old denials age out before the threshold.
func TestFloodWindowExpires(t *testing.T) {
	base := time.Unix(0, 0)
	l := New(Config{Rate: 1, Burst: 1})
	sink := &recordingSink{}
	l.SetFloodSink(FloodDetector{
		Threshold: 3, Window: 10 * time.Second, Recover: time.Minute,
	}, sink)

	l.Allow("t.d", base)
	l.Allow("t.d", base)                     // deny 1 at t=0
	l.Allow("t.d", base.Add(5*time.Second))  // token refills: admit
	l.Allow("t.d", base.Add(5*time.Second))  // deny 2 at t=5s
	l.Allow("t.d", base.Add(20*time.Second)) // refill: admit
	l.Allow("t.d", base.Add(20*time.Second)) // deny 3 at t=20s
	// The first two denials are older than the 10s window at +20s, so the
	// sliding count is only 1 — no flood despite three lifetime denials.
	if len(sink.started) != 0 {
		t.Fatalf("started = %d, want 0 after window expiry", len(sink.started))
	}
}

// TestFloodDisabledWithZeroThreshold confirms a zero detector is inert.
func TestFloodDisabledWithZeroThreshold(t *testing.T) {
	base := time.Unix(0, 0)
	l := New(Config{Rate: 1, Burst: 1})
	sink := &recordingSink{}
	l.SetFloodSink(FloodDetector{}, sink)
	l.Allow("t.d", base)
	for i := 0; i < 20; i++ {
		l.Allow("t.d", base)
	}
	if len(sink.started) != 0 || l.SweepFlood(base.Add(time.Hour), time.Hour) != 0 {
		t.Fatal("flood detection must be inert with zero threshold")
	}
}
