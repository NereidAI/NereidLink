// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// http_sink_test.go verifies webhook delivery and response classification.

package egress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newHTTPSink builds a webhook sink pointed at the given server URL.
func newHTTPSink(t *testing.T, url string, headers map[string]string) Sink {
	t.Helper()
	f := Forwarder{
		SinkType: SinkHTTP,
		SinkConfig: map[string]any{
			"url":     url,
			"method":  "",
			"headers": headers,
		},
	}
	sink, err := buildSink(f)
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}
	return sink
}

// TestHTTPSinkDelivered checks a 2xx response classifies as delivered.
func TestHTTPSinkDelivered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
	defer server.Close()

	sink := newHTTPSink(t, server.URL, nil)
	out := sink.Deliver(context.Background(), Envelope{ID: "1"})
	if out.Result != ResultDelivered {
		t.Fatalf("expected delivered, got %d (%s)", out.Result, out.Reason)
	}
	if out.Code != http.StatusNoContent {
		t.Fatalf("expected code %d, got %d", http.StatusNoContent, out.Code)
	}
}

func TestHTTPSinkIncludesBusinessChannel(t *testing.T) {
	var received Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sink := newHTTPSink(t, server.URL, nil)
	out := sink.Deliver(context.Background(), Envelope{ID: "1", Channel: "meter-reading.v1"})
	if out.Result != ResultDelivered {
		t.Fatalf("expected delivered, got %+v", out)
	}
	if received.Channel != "meter-reading.v1" {
		t.Fatalf("channel not delivered in envelope: %+v", received)
	}
}

// TestHTTPSinkRetryable checks 408, 429 and 5xx classify as retryable.
func TestHTTPSinkRetryable(t *testing.T) {
	for _, code := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	} {
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
		sink := newHTTPSink(t, server.URL, nil)
		out := sink.Deliver(context.Background(), Envelope{ID: "1"})
		server.Close()
		if out.Result != ResultRetryable {
			t.Fatalf("code %d should be retryable, got %d", code, out.Result)
		}
		if out.Code != code {
			t.Fatalf("expected code %d, got %d", code, out.Code)
		}
	}
}

// TestHTTPSinkTerminal checks other 4xx classify as terminal.
func TestHTTPSinkTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
	defer server.Close()

	sink := newHTTPSink(t, server.URL, nil)
	out := sink.Deliver(context.Background(), Envelope{ID: "1"})
	if out.Result != ResultTerminal {
		t.Fatalf("expected terminal, got %d", out.Result)
	}
	if out.Code != http.StatusBadRequest {
		t.Fatalf("expected code %d, got %d", http.StatusBadRequest, out.Code)
	}
}

// TestHTTPSinkRequiresURL verifies a missing URL fails construction.
func TestHTTPSinkRequiresURL(t *testing.T) {
	if _, err := buildSink(Forwarder{SinkType: SinkHTTP}); err == nil {
		t.Fatal("expected error for missing url")
	}
}

// TestHTTPSinkAddsHeaders verifies configured headers are sent.
func TestHTTPSinkAddsHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != "secret" {
				t.Errorf("custom header missing")
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type should be json")
			}
		}))
	defer server.Close()

	sink := newHTTPSink(t, server.URL, map[string]string{"X-Token": "secret"})
	_ = sink.Deliver(context.Background(), Envelope{ID: "1"})
}

// TestBackoffDelay verifies exponential growth, capping and a sane range.
func TestBackoffDelay(t *testing.T) {
	if d := backoffDelay(1); d < 1600*time.Millisecond || d > 2400*time.Millisecond {
		t.Fatalf("attempt 1 out of range: %v", d)
	}
	// High attempts cap the pre-jitter base at maxBackoff; jitter then
	// leaves the result within the 0.8..1.2 band around the cap.
	d := backoffDelay(10)
	if d < maxBackoff*4/5 {
		t.Fatalf("base delay should be capped, got %v", d)
	}
	if d > maxBackoff+maxBackoff/5 {
		t.Fatalf("jitter pushed delay above cap: %v", d)
	}
}
