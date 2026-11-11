// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// idempotency.go implements best-effort Idempotency-Key support for
// mutating REST calls (ADR-0017). A caller that retries the same write with
// the same key receives the original response instead of a duplicate side
// effect. The store is in-process with a bounded TTL; a restart loses the
// replay window, so this is a client-retry convenience, not a durable
// exactly-once guarantee.

package gatewayapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// idempotencyTTL bounds how long a completed request is replayed.
const idempotencyTTL = 10 * time.Minute

// idempotencyKeyTTL bounds entries that never completed so a slow write
// does not accumulate.
const idempotencyKeyTTL = 30 * time.Minute

// idempotencyEntry is one cached request/response pair.
type idempotencyEntry struct {
	requestHash string
	status      int
	body        []byte
	created     time.Time
}

// idempotencyStore is the in-process replay cache guarded by one mutex.
type idempotencyStore struct {
	mu      sync.Mutex
	entries map[string]idempotencyEntry
}

// newIdempotencyStore constructs an empty store.
func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{entries: map[string]idempotencyEntry{}}
}

// key scopes an Idempotency-Key to a caller scope and the request path.
func (s *idempotencyStore) key(scope, path, idemKey string) string {
	sum := sha256.Sum256([]byte(scope + "|" + path + "|" + idemKey))
	return hex.EncodeToString(sum[:])
}

// lookup returns a stored response when present and still valid.
func (s *idempotencyStore) lookup(cacheKey string) (idempotencyEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[cacheKey]
	if !ok {
		return idempotencyEntry{}, false
	}
	if time.Since(entry.created) > idempotencyTTL {
		delete(s.entries, cacheKey)
		return idempotencyEntry{}, false
	}
	return entry, true
}

// store records a completed response.
func (s *idempotencyStore) store(cacheKey, requestHash string, status int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) > 4096 {
		now := time.Now()
		for key, entry := range s.entries {
			if now.Sub(entry.created) > idempotencyKeyTTL {
				delete(s.entries, key)
			}
		}
	}
	s.entries[cacheKey] = idempotencyEntry{
		requestHash: requestHash,
		status:      status,
		body:        body,
		created:     time.Now(),
	}
}

// hashBody produces a stable fingerprint of a request body for conflict
// detection. Empty bodies hash deterministically.
func hashBody(body []byte) string {
	if len(body) == 0 {
		body = []byte("{}")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err == nil {
		body = compact.Bytes()
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// idempotentWriter captures the response for replay while forwarding to the
// underlying writer.
type idempotentWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

// WriteHeader records the status before forwarding.
func (w *idempotentWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the wrapped writer for trace-id chain traversal.
func (w *idempotentWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Write buffers the body while forwarding.
func (w *idempotentWriter) Write(p []byte) (int, error) {
	w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

// withIdempotency wraps a mutating handler with Idempotency-Key replay.
// scope uniquely identifies the caller (usually the principal or device),
// and path is the full request path so two resources do not collide.
func (s *idempotencyStore) withIdempotency(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || !mutatingMethod(r.Method) {
			next(w, r)
			return
		}
		cacheKey := s.key(scope, r.URL.Path, key)
		requestHash := hashBody(readBody(r))

		if existing, ok := s.lookup(cacheKey); ok {
			if existing.requestHash != requestHash {
				writeRESTError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT",
					"the Idempotency-Key was already used with a different request")
				return
			}
			writeRESTJSON(w, existing.status, json.RawMessage(existing.body))
			return
		}

		writer := &idempotentWriter{ResponseWriter: w, status: http.StatusOK}
		next(writer, r)
		if writer.status < 400 {
			s.store(cacheKey, requestHash, writer.status, writer.body.Bytes())
		}
	}
}

// readBody consumes and restores a request body for fingerprinting. It is
// safe only for handlers that re-read from r.Body afterwards.
func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}

// mutatingMethod reports whether the method can carry a write side effect.
func mutatingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete
}
