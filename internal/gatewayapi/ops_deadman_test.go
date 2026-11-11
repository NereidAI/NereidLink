// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ops_deadman_test.go covers the operator-triggered dead-man delivery
// test exposed on the loopback ops plane (ADR-0019). The point of the
// route is verifiability: an operator must be able to confirm the
// independent fallback endpoint is reachable without causing an outage.

package gatewayapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDeadman records reports and can be made to fail.
type fakeDeadman struct {
	code  string
	fail  bool
	calls int
}

func (f *fakeDeadman) Report(_ context.Context, code, _, _ string) error {
	f.calls++
	f.code = code
	if f.fail {
		return context.DeadlineExceeded
	}
	return nil
}

// TestOpsDeadmanTestDelivers verifies the happy path forwards the code.
func TestOpsDeadmanTestDelivers(t *testing.T) {
	hub := &Hub{}
	probe := &fakeDeadman{}
	hub.SetDeadmanProbe(probe)

	mux := http.NewServeMux()
	hub.RegisterOpsInitiator(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/deadman/test",
		strings.NewReader(`{"code":"CUSTOM"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if probe.calls != 1 || probe.code != "CUSTOM" {
		t.Fatalf("expected one CUSTOM report, got calls=%d code=%s", probe.calls, probe.code)
	}
}

// TestOpsDeadmanTestDefaultsCode verifies the default report code.
func TestOpsDeadmanTestDefaultsCode(t *testing.T) {
	hub := &Hub{}
	probe := &fakeDeadman{}
	hub.SetDeadmanProbe(probe)

	mux := http.NewServeMux()
	hub.RegisterOpsInitiator(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/deadman/test", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if probe.code != "PLATFORM_PROBLEM_TEST" {
		t.Fatalf("expected default code, got %s", probe.code)
	}
}

// TestOpsDeadmanTestDeliveryFailure verifies a channel failure surfaces
// as 502 so the operator learns the fallback is broken.
func TestOpsDeadmanTestDeliveryFailure(t *testing.T) {
	hub := &Hub{}
	hub.SetDeadmanProbe(&fakeDeadman{fail: true})

	mux := http.NewServeMux()
	hub.RegisterOpsInitiator(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/deadman/test", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}

// TestOpsDeadmanUnconfigured verifies 503 when the channel is absent.
func TestOpsDeadmanUnconfigured(t *testing.T) {
	hub := &Hub{}
	mux := http.NewServeMux()
	hub.RegisterOpsInitiator(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/deadman/test", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
