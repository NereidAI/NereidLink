// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// captcha.go implements the login arithmetic challenge. It is a lightweight
// anti-automation gate, not a cryptographic security boundary: the server
// issues an expression with a one-time id and consumes the entry exactly once
// when the answer is presented. The default storage is an in-process bounded
// TTL map because the platform carries no Redis; it targets single-process
// topologies. Cluster
// mode replaces it with a NATS-KV-backed instance (captcha_kv.go) so a
// challenge issued by one replica verifies on any other.

package gatewayapi

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// captchaTTL bounds how long a challenge stays solvable.
	captchaTTL = 10 * time.Minute
	// captchaMax bounds the in-memory fallback to prevent unbounded growth
	// if clients fetch challenges without ever answering.
	captchaMax = 1000
	// captchaOperandMax is the inclusive upper bound of operands; all
	// operands are positive and stay within 10.
	captchaOperandMax = 10
)

// captchaChallenger issues and one-time verifies arithmetic challenges.
// The in-memory captchaStore implements it for single-process deployments;
// kvCaptcha implements it on shared NATS KV storage in cluster mode.
type captchaChallenger interface {
	issue() (id, expression string)
	verify(id string, answer int) bool
}

// captchaEntry is a single pending challenge.
type captchaEntry struct {
	answer    int
	expiresAt time.Time
}

// captchaStore hands out and verifies one-time arithmetic challenges.
type captchaStore struct {
	mu      sync.Mutex
	entries map[string]captchaEntry
	now     func() time.Time
}

// Compile-time interface conformance for both implementations.
var (
	_ captchaChallenger = (*captchaStore)(nil)
	_ captchaChallenger = (*kvCaptcha)(nil)
)

// newCaptchaStore builds an empty store.
func newCaptchaStore() *captchaStore {
	return &captchaStore{
		entries: make(map[string]captchaEntry),
		now:     time.Now,
	}
}

// SetCaptchaStore replaces the default in-process challenge store with
// a shared (NATS-KV) one. Composition root only; safe to call before the
// HTTP server starts serving.
func (h *Hub) SetCaptchaStore(store captchaChallenger) {
	h.captcha = store
}

// randIntn returns a uniform random integer in [1,n].
func randIntn(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		// crypto/rand failure is not recoverable for a security-relevant
		// choice; fall back to the midpoint rather than a guessable seed.
		return n/2 + 1
	}
	return int(v.Int64()) + 1
}

// newID returns a random opaque challenge id.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(b)
}

// generate builds an operands/operator/answer triple using all four
// operations. Subtraction is forced non-negative and division is forced to
// divide evenly with a non-zero divisor, so the expression always has a
// single non-negative integer answer.
func generate() (expr string, answer int) {
	op := randIntn(4)
	switch op {
	case 1: // addition
		a, b := randIntn(captchaOperandMax), randIntn(captchaOperandMax)
		return strconv.Itoa(a) + " + " + strconv.Itoa(b), a + b
	case 2: // subtraction, order so the result is non-negative
		a, b := randIntn(captchaOperandMax), randIntn(captchaOperandMax)
		if b > a {
			a, b = b, a
		}
		return strconv.Itoa(a) + " - " + strconv.Itoa(b), a - b
	case 3: // multiplication, keep factors small enough the product is trivial
		a, b := randIntn(captchaOperandMax), randIntn(captchaOperandMax)
		return strconv.Itoa(a) + " × " + strconv.Itoa(b), a * b
	default: // division: choose divisor and quotient so dividend divides evenly
		divisor := randIntn(captchaOperandMax)
		// Largest quotient keeping dividend within the operand bound.
		maxQ := captchaOperandMax / divisor
		if maxQ < 1 {
			maxQ = 1
		}
		q := randIntn(maxQ)
		dividend := divisor * q
		return strconv.Itoa(dividend) + " ÷ " + strconv.Itoa(divisor), q
	}
}

// issue creates a challenge, stores it and returns its id plus expression.
func (s *captchaStore) issue() (id, expression string) {
	expr, answer := generate()
	id = newID()

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	// Opportunistically purge expired entries before inserting.
	for k, e := range s.entries {
		if e.expiresAt.Before(now) {
			delete(s.entries, k)
		}
	}
	s.entries[id] = captchaEntry{answer: answer, expiresAt: now.Add(captchaTTL)}
	// Bound growth by evicting arbitrary overflow entries.
	if len(s.entries) > captchaMax {
		for k := range s.entries {
			if len(s.entries) <= captchaMax {
				break
			}
			delete(s.entries, k)
		}
	}
	return id, expr + " = ?"
}

// verify consumes the challenge for id and reports whether answer matches.
// A missing, expired or already-consumed id resolves to false so a wrong or
// replayed answer cannot be retried against the same challenge.
func (s *captchaStore) verify(id string, answer int) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return false
	}
	delete(s.entries, id)
	if entry.expiresAt.Before(s.now()) {
		return false
	}
	return entry.answer == answer
}

// captchaView is the public challenge representation.
type captchaView struct {
	CaptchaID  string `json:"captcha_id"`
	Expression string `json:"expression"`
}

// handleGetCaptcha issues a fresh arithmetic challenge.
func (h *Hub) handleGetCaptcha(w http.ResponseWriter, r *http.Request) {
	id, expression := h.captcha.issue()
	writeRESTJSON(w, http.StatusOK, captchaView{CaptchaID: id, Expression: expression})
}
