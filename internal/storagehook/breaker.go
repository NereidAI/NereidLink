// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"errors"
	"sync"
	"time"
)

// Breaker tuning constants. Circuit breaking prevents a repeatedly failing
// external callback from consuming the per-hook timeout budget on every
// telemetry point, which would stall the storage writer under load.
const (
	// breakerFailureThreshold opens the breaker after consecutive failures.
	breakerFailureThreshold = 5

	// breakerCooldown is the open-state duration before a trial call.
	breakerCooldown = 30 * time.Second
)

// ErrCircuitOpen is returned while the breaker rejects calls.
var ErrCircuitOpen = errors.New("hook circuit breaker is open")

// breaker is a per-hook closed→open→half-open circuit breaker.
type breaker struct {
	mu sync.Mutex

	// failures counts consecutive failures in the closed state.
	failures int

	// openedAt records when the breaker entered the open state; zero means
	// the breaker is closed.
	openedAt time.Time

	// halfOpenInFlight ensures only one trial call probes recovery.
	halfOpenInFlight bool
}

// allow reports whether a call may proceed and, in half-open state, marks
// the single trial as in flight.
func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openedAt.IsZero() {
		return true
	}
	if now.Sub(b.openedAt) < breakerCooldown {
		return false
	}
	if b.halfOpenInFlight {
		return false
	}
	b.halfOpenInFlight = true
	return true
}

// recordSuccess closes the breaker after a successful call, including a
// successful half-open trial.
func (b *breaker) recordSuccess() {
	b.mu.Lock()
	b.failures = 0
	b.openedAt = time.Time{}
	b.halfOpenInFlight = false
	b.mu.Unlock()
}

// recordFailure opens the breaker when the failure threshold is reached. A
// failed half-open trial reopens immediately with a fresh cooldown.
func (b *breaker) recordFailure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.halfOpenInFlight = false
	b.failures++
	if b.failures >= breakerFailureThreshold {
		b.openedAt = now
	}
}
