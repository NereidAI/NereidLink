// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package ingestlimit implements the in-process per-device token bucket
// that sits in front of the message bus (ADR-0014 bombardment defense).
//
// Every device identity owns one lazily-created bucket refilled at a
// fixed rate up to a burst capacity. Frames arriving with no token
// available are rejected by the ingress and never reach JetStream. The
// limiter is deliberately local-memory only: it costs nothing on the
// hot path and bounds one device that floods an ingress. Cross-node
// tenant limits (a KV aggregate) are a separate, later layer.
package ingestlimit

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DefaultPerDeviceRate is the steady-state frames-per-second granted to
// one device when configuration names no rate.
const DefaultPerDeviceRate = 100.0

// DefaultBurstSeconds sizes the bucket as this many seconds of rate, so
// a device can absorb short spikes at the full ingest speed.
const DefaultBurstSeconds = 1.0

// FloodDetector turns raw frame rejections into device-level flood
// lifecycle events (ADR-0014): a device whose rejections cross a threshold
// inside a window raises one Start event, and a device that stays calm for
// a recovery window raises one Recover event. It never appears on the admit
// hot path for admitted frames and uses the same bounded, in-process map as
// the buckets, so it cannot grow without limit and never carries a
// device_id into a Prometheus label (that goes into the alarm instead).
type FloodDetector struct {
	// Threshold is the rejection count inside Window that opens a flood.
	Threshold int
	// Window bounds how far back rejections are counted.
	Window time.Duration
	// Recover is the calm interval after which an open flood clears.
	Recover time.Duration
}

// FloodEvent is one flood lifecycle transition for a limiter key.
type FloodEvent struct {
	// Key is the limiter key (tenant.device).
	Key string
	// Rejections is the count that opened the flood (Start only).
	Rejections int
	// At is the transition time.
	At time.Time

	// started distinguishes Start (true) from Recover (false).
	started bool
}

func (e FloodEvent) open() bool { return e.started }

// FloodSink consumes flood lifecycle events. A nil sink disables flood
// detection. Start fires once when a device crosses the threshold; Recover
// fires once after the calm window. Implementations must be non-blocking on
// the ingest hot path (e.g. hand off to a worker/channel).
type FloodSink interface {
	FloodStarted(ev FloodEvent)
	FloodRecovered(ev FloodEvent)
}

// floodState tracks one device's rejection history and open state.
type floodState struct {
	denials  []time.Time
	open     bool
	lastDeny time.Time
}

// bucket is one device token bucket. Refill is lazy: tokens are added
// from elapsed wall time only when Allow is called, which keeps idle
// devices free of background work.
type bucket struct {
	tokens float64
	cap    float64
	rate   float64
	last   time.Time
}

// allow reports whether one token can be consumed at now.
func (b *bucket) allow(now time.Time) bool {
	if b.rate <= 0 {
		// Zero or negative rate means unlimited.
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.cap {
			b.tokens = b.cap
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Config parameterizes a Limiter.
type Config struct {
	// Rate is steady-state tokens per second per device; <=0 = unlimited.
	Rate float64

	// Burst is the token capacity; <=0 uses Rate * DefaultBurstSeconds.
	Burst float64
}

// entry pairs a bucket with its last-use time for idle eviction.
type entry struct {
	bucket   *bucket
	lastSeen time.Time
}

// Limiter holds per-device buckets. It is safe for concurrent use.
type Limiter struct {
	rate float64
	cap  float64

	mu      sync.Mutex
	buckets map[string]entry

	// flood holds per-device flood state, keyed like buckets. It is only
	// allocated when a detector with a positive threshold is configured.
	flood map[string]*floodState
	det   FloodDetector
	sink  FloodSink

	// rejections counts frames denied, when metrics are wired.
	rejections prometheus.Counter
}

// SetMetrics registers the rejection counter.
func (l *Limiter) SetMetrics(counter prometheus.Counter) {
	l.rejections = counter
}

// SetFloodSink enables flood lifecycle detection. With a nil detector, a
// zero Threshold/Window or a nil sink, flood detection is a no-op.
func (l *Limiter) SetFloodSink(det FloodDetector, sink FloodSink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.det = det
	l.sink = sink
	if sink != nil && det.Threshold > 0 && det.Window > 0 {
		l.flood = map[string]*floodState{}
	} else {
		l.flood = nil
	}
}

// New constructs a Limiter.
func New(cfg Config) *Limiter {
	rate := cfg.Rate
	cap := cfg.Burst
	if cap <= 0 {
		cap = rate * DefaultBurstSeconds
	}
	return &Limiter{
		rate:    rate,
		cap:     cap,
		buckets: map[string]entry{},
	}
}

// Allow reports whether one frame from key (tenant.device) is admitted.
// Unlimited configuration always returns true. Buckets are created on
// first use.
func (l *Limiter) Allow(key string, now time.Time) bool {
	if l.rate <= 0 {
		return true
	}

	l.mu.Lock()
	e, ok := l.buckets[key]
	if !ok {
		e = entry{bucket: &bucket{
			tokens: l.cap,
			cap:    l.cap,
			rate:   l.rate,
			last:   now,
		}}
	}
	admitted := e.bucket.allow(now)
	e.lastSeen = now
	l.buckets[key] = e

	var transition *FloodEvent
	if !admitted {
		transition = l.recordDenialLocked(key, now)
	}
	l.mu.Unlock()

	if !admitted && l.rejections != nil {
		l.rejections.Inc()
	}
	if transition != nil && l.sink != nil {
		if transition.open() {
			l.sink.FloodStarted(*transition)
		} else {
			l.sink.FloodRecovered(*transition)
		}
	}
	return admitted
}

// recordDenialLocked appends a denial and opens a flood when the sliding
// window reaches the threshold. Caller holds l.mu.
func (l *Limiter) recordDenialLocked(key string, now time.Time) *FloodEvent {
	if l.flood == nil {
		return nil
	}
	st := l.flood[key]
	if st == nil {
		st = &floodState{}
		l.flood[key] = st
	}
	st.lastDeny = now

	// Once open, only the most recent denial matters for the recovery timer;
	// stop accumulating timestamps so a long flood cannot grow the slice.
	if st.open {
		return nil
	}
	cutoff := now.Add(-l.det.Window)
	kept := st.denials[:0]
	for _, t := range st.denials {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.denials = append(kept, now)

	if len(st.denials) >= l.det.Threshold {
		st.open = true
		n := len(st.denials)
		st.denials = nil
		return &FloodEvent{Key: key, Rejections: n, At: now, started: true}
	}
	return nil
}

// SweepFlood closes floods that have stayed calm for the recovery window and
// evicts flood state for buckets idle past idle. It returns the number of
// devices whose flood auto-cleared. Paced by the same periodic ticker as
// bucket eviction so a device that stops flooding clears without sending a
// new frame.
func (l *Limiter) SweepFlood(now time.Time, idle time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flood == nil {
		return 0
	}
	recovered := 0
	var events []FloodEvent
	for key, st := range l.flood {
		switch {
		case st.open && now.Sub(st.lastDeny) >= l.det.Recover:
			st.open = false
			events = append(events, FloodEvent{Key: key, At: now, started: false})
			recovered++
			delete(l.flood, key)
		case now.Sub(st.lastDeny) > idle:
			delete(l.flood, key)
		}
	}
	if l.sink != nil {
		for i := range events {
			l.sink.FloodRecovered(events[i])
		}
	}
	return recovered
}

// Sweep evicts buckets unused for longer than idle, bounding memory as
// devices disconnect. A full burst of tokens after the idle window means
// the device can start fresh on its next frame.
func (l *Limiter) Sweep(now time.Time, idle time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for key, e := range l.buckets {
		if now.Sub(e.lastSeen) > idle {
			delete(l.buckets, key)
			removed++
		}
	}
	return removed
}

// Count reports the number of held buckets.
func (l *Limiter) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
