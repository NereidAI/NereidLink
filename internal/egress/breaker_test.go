// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// breaker_test.go verifies the per-forwarder circuit breaker state machine.

package egress

import (
	"testing"
	"time"
)

// newTestBreaker builds a breaker with a controllable clock and short
// cooldown.
func newTestBreaker(t *testing.T, threshold int, cooldown time.Duration) (*breaker, func(time.Duration)) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	b := &breaker{
		threshold: threshold,
		cooldown:  cooldown,
		state:     stateClosed,
		now:       func() time.Time { return now },
	}
	advance := func(d time.Duration) { now = now.Add(d) }
	return b, advance
}

// TestBreakerOpensAfterThreshold checks a run of failures opens the breaker
// and blocks further calls until the cooldown elapses.
func TestBreakerOpensAfterThreshold(t *testing.T) {
	b, advance := newTestBreaker(t, 3, 30*time.Second)

	for i := 0; i < 2; i++ {
		b.recordResult(false)
		if !b.allow() {
			t.Fatalf("call %d should be allowed before threshold", i)
		}
	}
	b.recordResult(false)
	if b.allow() {
		t.Fatal("breaker should be open after threshold failures")
	}

	advance(29 * time.Second)
	if b.allow() {
		t.Fatal("breaker should stay open within cooldown")
	}
	advance(time.Second)
	if !b.allow() {
		t.Fatal("breaker should admit a trial after cooldown")
	}
}

// TestBreakerTrialSuccessCloses verifies a successful half-open trial
// resets the breaker to closed.
func TestBreakerTrialSuccessCloses(t *testing.T) {
	b, advance := newTestBreaker(t, 1, time.Second)

	b.recordResult(false)
	if b.allow() {
		t.Fatal("breaker should be open")
	}
	advance(time.Second)
	if !b.allow() {
		t.Fatal("expected half-open trial")
	}
	b.recordResult(true)
	if !b.allow() {
		t.Fatal("breaker should be closed after a successful trial")
	}
}

// TestBreakerTrialFailureReopens verifies a failed trial reopens the
// breaker for a fresh cooldown.
func TestBreakerTrialFailureReopens(t *testing.T) {
	b, advance := newTestBreaker(t, 1, time.Minute)

	b.recordResult(false)
	advance(time.Minute)
	if !b.allow() {
		t.Fatal("expected half-open trial")
	}
	b.recordResult(false)
	if b.allow() {
		t.Fatal("failed trial should reopen the breaker")
	}
}
