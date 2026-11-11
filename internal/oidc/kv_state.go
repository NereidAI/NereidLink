// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// kvLoginStateBucket holds in-flight OIDC authorization transactions shared
// by every serve replica.
const kvLoginStateBucket = "NL_OIDC_LOGIN_STATE"

// StateStore is the single-use, TTL-bounded store for authorization-code
// transactions. The default in-memory implementation targets one process;
// kvStateStore backs multi-replica deployments.
type StateStore interface {
	put(key string, st loginState) error
	take(key string) (loginState, bool)
	len() int
}

// kvState is a JetStream KV backed StateStore. Consumption is atomic: take
// deletes with an expected-revision guard, so two replicas processing the
// same browser callback cannot both win. Bucket TTL bounds stale rows.
type kvState struct {
	kv  jetstream.KeyValue
	ttl time.Duration
}

// kvEnvelope is the JSON value stored per state key.
type kvEnvelope struct {
	Nonce        string    `json:"nonce"`
	CodeVerifier string    `json:"code_verifier"`
	RedirectTo   string    `json:"redirect_to"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// NewKVStateStore creates or updates the shared login-state bucket.
func NewKVStateStore(ctx context.Context, js jetstream.JetStream, replicas int, ttl time.Duration) (StateStore, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if replicas < 1 {
		replicas = 1
	}
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   kvLoginStateBucket,
		TTL:      ttl,
		Replicas: replicas,
		History:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure oidc login-state KV: %w", err)
	}
	return &kvState{kv: kv, ttl: ttl}, nil
}

func (s *kvState) put(key string, st loginState) error {
	// Use a detached context: the HTTP request context is cancelled once the
	// 302 response to the browser is written, before this KV put would be
	// allowed to fail the flow.
	body, err := json.Marshal(kvEnvelope{
		Nonce:        st.nonce,
		CodeVerifier: st.codeVerifier,
		RedirectTo:   st.redirectTo,
		ExpiresAt:    st.expiresAt,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.kv.Put(ctx, key, body); err != nil {
		return fmt.Errorf("store oidc login state: %w", err)
	}
	return nil
}

func (s *kvState) take(key string) (loginState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entry, err := s.kv.Get(ctx, key)
	if err != nil {
		return loginState{}, false
	}
	var env kvEnvelope
	if err := json.Unmarshal(entry.Value(), &env); err != nil {
		return loginState{}, false
	}
	if time.Now().After(env.ExpiresAt) {
		return loginState{}, false
	}
	// Atomic single-use: only the replica holding the current revision can
	// delete; a concurrent callback on another peer loses the guard.
	if err := s.kv.Delete(ctx, key, jetstream.LastRevision(entry.Revision())); err != nil {
		return loginState{}, false
	}
	return loginState{
		nonce:        env.Nonce,
		codeVerifier: env.CodeVerifier,
		redirectTo:   env.RedirectTo,
		expiresAt:    env.ExpiresAt,
	}, true
}

func (s *kvState) len() int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		// A bucket with no keys returns an error from some servers.
		return 0
	}
	return len(keys)
}
