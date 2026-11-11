// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// deadman_test.go covers the independent platform fallback channel:
// threshold-based reporting, one-shot raise/clear semantics and recovery.

package deadman

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingWebhook captures dead-man reports.
type recordingWebhook struct {
	mu      sync.Mutex
	reports []map[string]string
}

func (r *recordingWebhook) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		r.mu.Lock()
		r.reports = append(r.reports, m)
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *recordingWebhook) codes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.reports))
	for _, m := range r.reports {
		out = append(out, m["code"])
	}
	return out
}

// TestMonitorThresholdAndRecovery verifies that reporting happens only
// after the failure threshold and recovery fires exactly once.
func TestMonitorThresholdAndRecovery(t *testing.T) {
	rec := &recordingWebhook{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	notifier := NewNotifier(Config{Enabled: true, WebhookURL: srv.URL, Timeout: 2 * time.Second})
	var down int32
	probe := Probe{Name: "postgres", Check: func(context.Context) error {
		if atomic.LoadInt32(&down) == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}}
	m := NewMonitor(notifier, slog.New(slog.NewTextHandler(io.Discard, nil)), probe)
	m.WithSchedule(time.Hour, 3)

	// One failure: no report yet.
	atomic.StoreInt32(&down, 1)
	m.runOnce(context.Background())
	if got := rec.codes(); len(got) != 0 {
		t.Fatalf("no report before threshold, got %v", got)
	}
	// Third consecutive failure crosses the threshold.
	m.runOnce(context.Background())
	m.runOnce(context.Background())
	if got := rec.codes(); len(got) != 1 || got[0] != "PLATFORM_PROBLEM" {
		t.Fatalf("expected one PLATFORM_PROBLEM, got %v", got)
	}
	// Further failures do not re-report while still down.
	m.runOnce(context.Background())
	if got := rec.codes(); len(got) != 1 {
		t.Fatalf("must not re-report while down, got %v", got)
	}
	// Recovery reports once.
	atomic.StoreInt32(&down, 0)
	m.runOnce(context.Background())
	if got := rec.codes(); len(got) != 2 || got[1] != "PLATFORM_PROBLEM_CLEARED" {
		t.Fatalf("expected a cleared report, got %v", got)
	}
	// Healthy again: no extra report.
	m.runOnce(context.Background())
	if got := rec.codes(); len(got) != 2 {
		t.Fatalf("stable healthy must not report, got %v", got)
	}
}

// TestNotifierDisabledIsNoop ensures a disabled notifier never delivers.
func TestNotifierDisabledIsNoop(t *testing.T) {
	rec := &recordingWebhook{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	m := NewMonitor(NewNotifier(Config{Enabled: false, WebhookURL: srv.URL}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Probe{Name: "always-fail", Check: func(context.Context) error { return context.DeadlineExceeded }})
	m.WithSchedule(time.Hour, 1)
	for i := 0; i < 3; i++ {
		m.runOnce(context.Background())
	}
	if got := rec.codes(); len(got) != 0 {
		t.Fatalf("disabled notifier must not deliver, got %v", got)
	}
}

// TestReportWebhookError surfaces delivery failures to the caller.
func TestReportWebhookError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	n := NewNotifier(Config{Enabled: true, WebhookURL: srv.URL, Timeout: time.Second})
	if err := n.Report(context.Background(), "PLATFORM_PROBLEM", "s", "b"); err == nil {
		t.Fatal("expected delivery error for 500 response")
	}
}
