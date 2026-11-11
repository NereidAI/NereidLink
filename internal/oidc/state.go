// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"sync"
	"time"
)

// stateStore is a bounded, in-process map of in-flight authorization-code
// transactions. The all-in-one topology has one process; a multi-node
// deployment would back this with NATS KV (later). Entries are single-use and
// expire by TTL, so the map cannot grow: a background sweep reclaims stale
// rows and the cap rejects runaway allocation.
type stateStore struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]loginState
}

// maxInFlight bounds concurrent uncompleted logins.
const maxInFlight = 10_000

func newStateStore(ttl time.Duration) *stateStore {
	s := &stateStore{ttl: ttl, entries: map[string]loginState{}}
	return s
}

func (s *stateStore) put(key string, st loginState) error {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic sweep keeps the map bounded without a goroutine.
	if len(s.entries) >= maxInFlight {
		for k, e := range s.entries {
			if now.After(e.expiresAt) {
				delete(s.entries, k)
			}
		}
	}
	s.entries[key] = st
	return nil
}

// take removes and returns a state entry, rejecting unknown or expired rows.
func (s *stateStore) take(key string) (loginState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.entries[key]
	if !ok {
		return loginState{}, false
	}
	delete(s.entries, key)
	if time.Now().After(st.expiresAt) {
		return loginState{}, false
	}
	return st, true
}

// len is used by tests.
func (s *stateStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
