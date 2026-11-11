// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// trace_test.go verifies the request-correlation middleware (ADR-0017):
// an inbound X-Request-ID is echoed, a missing one is generated, and error
// envelopes carry a trace ID.

package gatewayapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// failAlways writes an error envelope so its trace ID can be inspected.
func failAlways(w http.ResponseWriter, _ *http.Request) {
	writeRESTError(w, http.StatusBadRequest, "TEST_ERROR", "boom")
}

// TestTraceMiddlewareEchoesInboundID verifies caller-supplied trace IDs
// propagate to the error envelope.
func TestTraceMiddlewareEchoesInboundID(t *testing.T) {
	handler := traceMiddleware(http.HandlerFunc(failAlways))
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.Header.Set("X-Request-ID", "caller-trace-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body struct {
		Error struct {
			TraceID string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.TraceID != "caller-trace-1" {
		t.Fatalf("trace = %q", body.Error.TraceID)
	}
}

// TestTraceMiddlewareGeneratesID verifies a generated ID is returned.
func TestTraceMiddlewareGeneratesID(t *testing.T) {
	handler := traceMiddleware(http.HandlerFunc(failAlways))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x", nil))

	var body struct {
		Error struct {
			TraceID string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Error.TraceID) != 32 {
		t.Fatalf("trace = %q", body.Error.TraceID)
	}
}

// TestTraceWithoutMiddlewareIsEmpty ensures the envelope is still valid
// when the middleware is absent.
func TestTraceWithoutMiddlewareIsEmpty(t *testing.T) {
	recorder := httptest.NewRecorder()
	http.HandlerFunc(failAlways).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/x", nil))
	var body struct {
		Error struct {
			TraceID string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.TraceID != "" {
		t.Fatalf("trace = %q, want empty", body.Error.TraceID)
	}
}
