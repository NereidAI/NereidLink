// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// captcha_kv.go backs the login arithmetic challenge with a NATS KV bucket
// so a challenge issued by one serve replica verifies on every other
// replica. The single-pod topologies keep the in-process store
// (captcha.go); cluster mode wires this shared tier instead, alongside
// the OIDC login-state bucket (ADR-0021 HA wiring). Consumption is
// atomic: verify reads the current revision and deletes with an
// expected-revision guard, so a replayed answer cannot win.

package gatewayapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// kvCaptchaBucket holds pending login challenges shared by all replicas.
const kvCaptchaBucket = "NL_CAPTCHA"

// kvCaptchaEnvelope is the JSON value stored per challenge id.
type kvCaptchaEnvelope struct {
	Answer    int       `json:"answer"`
	ExpiresAt time.Time `json:"expires_at"`
}

// kvCaptcha is a captchaChallenger backed by JetStream key-value.
type kvCaptcha struct {
	kv  jetstream.KeyValue
	ttl time.Duration
	now func() time.Time
}

// NewKVCaptchaStore creates or updates the shared captcha bucket. ttl
// defaults to captchaTTL and replicas is the JetStream Raft factor.
func NewKVCaptchaStore(ctx context.Context, js jetstream.JetStream, replicas int, ttl time.Duration) (captchaChallenger, error) {
	if ttl <= 0 {
		ttl = captchaTTL
	}
	if replicas < 1 {
		replicas = 1
	}
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   kvCaptchaBucket,
		TTL:      ttl,
		Replicas: replicas,
		History:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure captcha KV: %w", err)
	}
	return &kvCaptcha{kv: kv, ttl: ttl, now: time.Now}, nil
}

// issue builds a challenge and stores the answer in the shared bucket.
func (s *kvCaptcha) issue() (id, expression string) {
	expr, answer := generate()
	id = newID()

	body, err := json.Marshal(kvCaptchaEnvelope{
		Answer:    answer,
		ExpiresAt: s.now().Add(s.ttl),
	})
	if err != nil {
		// generate() output cannot fail to marshal; keep fail-closed.
		return id, expr + " = ?"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.kv.Put(ctx, id, body); err != nil {
		// Fail closed: the id is deliberately absent from the store, so
		// the answer cannot verify on any replica during a KV outage.
		slog.ErrorContext(ctx, "captcha: store challenge in KV", "error", err)
	}
	return id, expr + " = ?"
}

// verify consumes the challenge for id and reports whether answer
// matches. A missing, expired or already-consumed id resolves to false.
func (s *kvCaptcha) verify(id string, answer int) bool {
	if id == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	entry, err := s.kv.Get(ctx, id)
	if err != nil {
		return false
	}
	var env kvCaptchaEnvelope
	if err := json.Unmarshal(entry.Value(), &env); err != nil {
		return false
	}
	// Atomic single-use: only the replica holding the current revision
	// can delete; a concurrent or replayed verify loses the guard.
	if err := s.kv.Delete(ctx, id, jetstream.LastRevision(entry.Revision())); err != nil {
		return false
	}
	if s.now().After(env.ExpiresAt) {
		return false
	}
	return env.Answer == answer
}
