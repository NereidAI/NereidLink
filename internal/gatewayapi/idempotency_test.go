// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// idempotency_test.go verifies Idempotency-Key replay and conflict
// handling (ADR-0017).

package gatewayapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// countingWriter increments on every successful write so duplicates are
// observable.
func countingHandler(store *idempotencyStore, calls *int) http.HandlerFunc {
	return store.withIdempotency("test", func(w http.ResponseWriter, r *http.Request) {
		*calls++
		writeRESTJSON(w, http.StatusCreated, map[string]string{"id": "obj-1"})
	})
}

// TestIdempotencyReplay verifies a repeated key returns the cached body
// without invoking the handler twice.
func TestIdempotencyReplay(t *testing.T) {
	store := newIdempotencyStore()
	calls := 0
	handler := countingHandler(store, &calls)

	body := []byte(`{"name":"x"}`)
	first := httptest.NewRequest(http.MethodPost, "/v1/t/devices", bytes.NewReader(body))
	first.Header.Set("Idempotency-Key", "k-1")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, first)

	second := httptest.NewRequest(http.MethodPost, "/v1/t/devices", bytes.NewReader(body))
	second.Header.Set("Idempotency-Key", "k-1")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, second)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if firstRecorder.Body.String() != secondRecorder.Body.String() {
		t.Fatalf("replay mismatch:\n%s\n%s", firstRecorder.Body.String(), secondRecorder.Body.String())
	}
	if secondRecorder.Code != http.StatusCreated {
		t.Fatalf("replay status = %d", secondRecorder.Code)
	}
}

// TestIdempotencyConflict verifies a different body with the same key is
// rejected instead of being replayed.
func TestIdempotencyConflict(t *testing.T) {
	store := newIdempotencyStore()
	calls := 0
	handler := countingHandler(store, &calls)

	first := httptest.NewRequest(http.MethodPost, "/v1/t/devices",
		bytes.NewReader([]byte(`{"name":"a"}`)))
	first.Header.Set("Idempotency-Key", "k-2")
	handler.ServeHTTP(httptest.NewRecorder(), first)

	second := httptest.NewRequest(http.MethodPost, "/v1/t/devices",
		bytes.NewReader([]byte(`{"name":"b"}`)))
	second.Header.Set("Idempotency-Key", "k-2")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, second)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// TestIdempotencyIgnoredWithoutKey verifies normal requests bypass the
// replay logic.
func TestIdempotencyIgnoredWithoutKey(t *testing.T) {
	store := newIdempotencyStore()
	calls := 0
	handler := countingHandler(store, &calls)

	request := httptest.NewRequest(http.MethodPost, "/v1/t/devices",
		bytes.NewReader([]byte(`{"name":"x"}`)))
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}
