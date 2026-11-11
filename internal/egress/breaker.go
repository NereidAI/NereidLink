// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// breaker.go is a small per-forwarder circuit breaker. It stops the worker
// from hammering a receiver that is failing continuously: after a run of
// failures it opens for a cooldown, then admits one trial call to probe
// recovery (half-open). A successful trial closes it; a failed trial reopens
// it.

package egress

import (
	"sync"
	"time"
)

// Default breaker tuning, shared unless the runtime overrides it.
const (
	// breakerThreshold is the consecutive failures that open the breaker.
	breakerThreshold = 5
	// breakerCooldown is how long delivery pauses before a trial is allowed.
	breakerCooldown = 30 * time.Second
)

// breaker states.
type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// breaker is a guarded three-state circuit breaker.
type breaker struct {
	threshold int
	cooldown  time.Duration

	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt time.Time
	now      func() time.Time
}

// newBreaker constructs a breaker with default thresholds.
func newBreaker() *breaker {
	return &breaker{
		threshold: breakerThreshold,
		cooldown:  breakerCooldown,
		state:     stateClosed,
		now:       time.Now,
	}
}

// allow reports whether a delivery may proceed. When open and the cooldown
// elapsed it transitions to half-open and admits exactly the next call.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateClosed:
		return true
	case stateOpen:
		if b.now().Sub(b.openedAt) >= b.cooldown {
			b.state = stateHalfOpen
			return true
		}
		return false
	default: // half-open admits only the single in-flight trial.
		return false
	}
}

// recordResult updates the breaker from a delivery outcome.
func (b *breaker) recordResult(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ok {
		b.state = stateClosed
		b.failures = 0
		return
	}

	switch b.state {
	case stateHalfOpen:
		// The one trial failed; reopen for another cooldown.
		b.state = stateOpen
		b.openedAt = b.now()
	case stateClosed:
		b.failures++
		if b.failures >= b.threshold {
			b.state = stateOpen
			b.openedAt = b.now()
		}
	case stateOpen:
		// Failures observed while open keep it open.
		b.openedAt = b.now()
	}
}
