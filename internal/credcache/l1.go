// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// l1.go implements the process-local tier with ristretto.
//
// Ristretto admission is asynchronous, so Set is followed by Wait to make
// a just-stored entry visible deterministically; the wait costs microseconds
// and keeps the authentication semantics predictable.

package credcache

import (
	"time"

	"github.com/dgraph-io/ristretto"
)

// L1Config parameterizes the process-local cache.
type L1Config struct {
	// MaxCost is the approximate byte budget; <=0 uses the default.
	MaxCost int64
	// NumCounters is the admission counter count; <=0 uses the default.
	NumCounters int64
}

// DefaultL1MaxCost is the approximate memory budget (100 MiB) used when
// configuration names no value.
const DefaultL1MaxCost int64 = 100 << 20

// DefaultL1NumCounters bounds TinyLFU admission bookkeeping.
const DefaultL1NumCounters int64 = 1_000_000

// l1Cache wraps ristretto for cached entries.
type l1Cache struct {
	cache *ristretto.Cache
}

// L1Cache is the exported alias for composition-root wiring.
type L1Cache = l1Cache

// NewL1 builds the process-local cache.
func NewL1(cfg L1Config) (*l1Cache, error) {
	maxCost := cfg.MaxCost
	if maxCost <= 0 {
		maxCost = DefaultL1MaxCost
	}
	numCounters := cfg.NumCounters
	if numCounters <= 0 {
		numCounters = DefaultL1NumCounters
	}
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: numCounters,
		MaxCost:     maxCost,
		// Buffer items briefly; Wait after Set provides deterministic
		// visibility without a large buffer.
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}
	return &l1Cache{cache: cache}, nil
}

// get returns the entry for credentialsID.
func (l *l1Cache) get(credentialsID string) (*Entry, bool) {
	value, ok := l.cache.Get(credentialsID)
	if !ok {
		return nil, false
	}
	entry, ok := value.(*Entry)
	return entry, ok
}

// set stores the entry with an explicit TTL.
func (l *l1Cache) set(credentialsID string, entry *Entry, ttl time.Duration) {
	l.cache.SetWithTTL(credentialsID, entry, l.cost(entry), ttl)
	// Block until the item is admitted so the next read on the same
	// connection path observes it.
	l.cache.Wait()
}

// delete removes one entry.
func (l *l1Cache) delete(credentialsID string) {
	l.cache.Del(credentialsID)
	l.cache.Wait()
}

// clear empties the tier, used by the emergency epoch fence.
func (l *l1Cache) clear() {
	l.cache.Clear()
	l.cache.Wait()
}

// cost approximates the memory footprint of one entry for the ristretto
// budget: secret material plus a fixed per-entry overhead.
func (l *l1Cache) cost(entry *Entry) int64 {
	return int64(len(entry.Record.Secret)+len(entry.Record.Extra)) + 128
}
