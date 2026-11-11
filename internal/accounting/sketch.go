// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sketch.go implements the bounded local heavy-hitter structure used by
// the ADR-0025 resource accounting path (sFlow/"show CLI" style
// accounting).
//
// Sketch is a Space-Saving heavy-hitter estimator with a hard capacity. It
// answers "which dimension values dominate this window?" with a documented
// (count-error, count) guarantee while using memory proportional to
// capacity, never to stream length.
//
// A Count-Min Sketch was evaluated alongside it and deliberately left out:
// it can only answer "how much did this value contribute" for a value the
// caller already knows, so it cannot add a candidate the bounded sketch
// evicted, and the report shape needs enumerable candidates.
//
// Nothing in this file allocates per observed event beyond the map entry
// of a Space-Saving candidate, because it runs on the ingest hot path.

package accounting

import (
	"hash/maphash"
	"math"
	"sort"
	"sync"
)

// DefaultSketchCapacity is the per-dimension candidate budget. One
// candidate costs a map entry plus a counter, so 256 dimensions stay in
// the low tens of kilobytes while still covering the overwhelming
// majority of real tenants and devices.
const DefaultSketchCapacity = 256

// Candidate is one heavy-hitter entry. Count is the Space-Saving estimate
// and Error is the maximum amount by which that estimate may exceed the
// true count; the true count is guaranteed to be at least Count-Error.
type Candidate struct {
	// Value is the dimension value.
	Value string
	// Count is the estimated occurrence count.
	Count uint64
	// Error is the Space-Saving error bound for this entry.
	Error uint64
	// Bytes is the accumulated payload size observed for this value.
	Bytes int64
}

// MinCount is the guaranteed lower bound of the true count.
func (c Candidate) MinCount() uint64 {
	if c.Count <= c.Error {
		return 0
	}
	return c.Count - c.Error
}

// Sketch is a bounded Space-Saving heavy-hitter estimator. It is safe for
// concurrent use: the ingest hot path records from many connections while
// the reporter reads a snapshot.
type Sketch struct {
	mu       sync.Mutex
	capacity int
	seed     maphash.Seed
	entries  map[string]*sketchEntry
	// samples counts every observed value, including values that lost
	// their slot, so an operator can see how much of the stream the
	// bounded structure could not track.
	samples uint64
}

// sketchEntry is the mutable counter of one candidate.
type sketchEntry struct {
	count uint64
	// err is the count of the entry that was evicted (or displaced) when
	// this candidate was admitted. Space-Saving guarantees the true count
	// is at least count-err.
	err uint64
	// bytes accumulates the payload size attributed to this value. It is
	// tracked per candidate rather than per dimension because byte weight
	// is a property of the value: a dimension total divided by the
	// candidate count would report a meaningless average.
	bytes int64
}

// NewSketch builds a heavy-hitter sketch. A non-positive capacity falls
// back to DefaultSketchCapacity so a misconfigured reporter can never
// allocate an unbounded map.
func NewSketch(capacity int) *Sketch {
	if capacity <= 0 {
		capacity = DefaultSketchCapacity
	}
	return &Sketch{
		capacity: capacity,
		seed:     maphash.MakeSeed(),
		entries:  make(map[string]*sketchEntry, capacity),
	}
}

// Capacity returns the candidate budget.
func (s *Sketch) Capacity() int {
	return s.capacity
}

// Observe records one occurrence of value with no payload weight.
func (s *Sketch) Observe(value string) {
	s.observe(value, 0)
}

// ObserveWeighted records one occurrence of value carrying bytes of
// payload. Callers on the ingest hot path use this form so byte volume
// stays attributable to the value that caused it.
func (s *Sketch) ObserveWeighted(value string, bytes int64) {
	s.observe(value, bytes)
}

// observe records one occurrence. Callers must hold no lock; this is the
// single mutation path.
func (s *Sketch) observe(value string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples++
	if entry, ok := s.entries[value]; ok {
		// Counts saturate rather than wrap: an overflowed counter would
		// wrap to a small value, which would demote a genuinely dominant
		// candidate and could even rank it below a cold one.
		if entry.count < math.MaxUint64 {
			entry.count++
		}
		entry.bytes = saturateAdd(entry.bytes, bytes)
		return
	}
	if len(s.entries) < s.capacity {
		s.entries[value] = &sketchEntry{count: 1, bytes: bytes}
		return
	}
	// The structure is full: evict the minimum and admit the newcomer with
	// the minimum as its error bound. This is the Space-Saving admission
	// rule and it is what keeps memory flat under unbounded cardinality.
	minValue, minEntry := s.minimumLocked()
	// The evicted key's true count is minEntry.count, so charging that as
	// the newcomer's error preserves the "no undercount beyond the
	// minimum" guarantee.
	delete(s.entries, minValue)
	s.entries[value] = &sketchEntry{count: minEntry.count + 1, err: minEntry.count, bytes: bytes}
}

// minimumLocked returns the candidate with the lowest count. Callers must
// hold the lock. Ties break on the value so eviction is deterministic and
// therefore reproducible across replicas.
func (s *Sketch) minimumLocked() (string, *sketchEntry) {
	minValue := ""
	var minEntry *sketchEntry
	for value, entry := range s.entries {
		if minEntry == nil || entry.count < minEntry.count ||
			(entry.count == minEntry.count && value < minValue) {
			minValue, minEntry = value, entry
		}
	}
	return minValue, minEntry
}

// Samples returns the total number of observed values, including those
// that never won a slot.
func (s *Sketch) Samples() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.samples
}

// Tracked returns the number of live candidates.
func (s *Sketch) Tracked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Top returns up to limit candidates ordered by descending estimated
// count. Ties break on the value so the output is stable across calls,
// which keeps the diagnostic API and its tests deterministic.
func (s *Sketch) Top(limit int) []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.entries) {
		limit = len(s.entries)
	}
	out := make([]Candidate, 0, limit)
	for value, entry := range s.entries {
		out = append(out, Candidate{
			Value: value, Count: entry.count, Error: entry.err, Bytes: entry.bytes,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out[:limit]
}

// Lookup returns the candidate for value, if it currently holds a slot.
func (s *Sketch) Lookup(value string) (Candidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[value]
	if !ok {
		return Candidate{}, false
	}
	return Candidate{Value: value, Count: entry.count, Error: entry.err, Bytes: entry.bytes}, true
}

// Reset clears every candidate and the observation total.
//
// The accounting window is a half-open interval, and consecutive reports
// cover consecutive disjoint intervals, so a report must contain exactly
// the traffic of its own window. Resetting — rather than decaying — is what
// makes the reports additive: the diagnostic API sums rows across windows,
// and a decaying counter would count the same frame again in every
// subsequent window, inflating the total by roughly 1/(1-decay).
//
// Decay would be the right tool for a sliding-window view held in
// process, but windowing here is the store's job (a SQL range scan), so
// the in-process structure only has to be correct within one window.
func (s *Sketch) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
	s.samples = 0
}

// saturateAdd adds two byte totals, clamping at the signed maximum. A
// frame size is bounded by the ingress limit, but a very long window can
// still accumulate past int64 on a single hot value, and a wrapped sum
// would report negative traffic.
func saturateAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// saturateMul multiplies two byte counts, clamping at the signed maximum.
// A frame size multiplied by the sampling divisor can exceed int64 on a
// pathological input, and a wrapped result would report negative traffic.
func saturateMul(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// saturateMulCount scales an unsigned heavy-hitter count by the sampling
// divisor and clamps it into the uint64 range the report carries.
func saturateMulCount(count uint64, rate int64) uint64 {
	if count == 0 || rate <= 0 {
		return 0
	}
	if uint64(rate) > math.MaxUint64/count {
		return math.MaxUint64
	}
	return count * uint64(rate)
}
