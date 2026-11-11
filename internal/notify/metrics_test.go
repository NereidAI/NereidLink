// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metrics_test.go verifies ADR-0019 delivery instrumentation: per-channel
// success/failure counters plus the channel-call and trigger->deliver
// latency histograms. It drives the real webhook channel against an
// httptest server, so no database is required.

package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads the current value of one labeled counter.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// histCount reads the sample count of a labeled histogram.
func histCount(t *testing.T, h prometheus.Observer) uint64 {
	t.Helper()
	var m dto.Metric
	if err := h.(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

// TestDeliveryMetricsRecordsSuccessAndFailure sends one successful and
// one failing webhook delivery and asserts counters and histograms.
func TestDeliveryMetricsRecordsSuccessAndFailure(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	m := newDeliveryMetrics()
	svc := &Service{cfg: Config{DeliveryTimeout: 2 * time.Second}, metrics: m}
	ctx := context.Background()
	started := time.Now().Add(-50 * time.Millisecond)

	svc.deliver(ctx, Message{
		Channel: KindWebhook, Target: ok.URL, Severity: "CRITICAL",
		AlarmStartedAt: started,
	})
	svc.deliver(ctx, Message{
		Channel: KindWebhook, Target: bad.URL, Severity: "WARNING",
		AlarmStartedAt: started,
	})

	success := m.deliveries.WithLabelValues(KindWebhook, "CRITICAL", resultSuccess)
	failed := m.deliveries.WithLabelValues(KindWebhook, "WARNING", resultFailed)
	if got := counterValue(t, success); got != 1 {
		t.Fatalf("success counter = %v, want 1", got)
	}
	if got := counterValue(t, failed); got != 1 {
		t.Fatalf("failed counter = %v, want 1", got)
	}

	// One channel-call sample per delivery for each severity.
	if got := histCount(t, m.channelCall.WithLabelValues(KindWebhook, "CRITICAL")); got != 1 {
		t.Fatalf("critical channel-call samples = %d, want 1", got)
	}
	if got := histCount(t, m.channelCall.WithLabelValues(KindWebhook, "WARNING")); got != 1 {
		t.Fatalf("warning channel-call samples = %d, want 1", got)
	}
	// The trigger->deliver histogram only fills when a start time is set.
	if got := histCount(t, m.triggerToDeliver.WithLabelValues(KindWebhook, "CRITICAL")); got != 1 {
		t.Fatalf("critical trigger->deliver samples = %d, want 1", got)
	}
}

// TestTriggerLatencySkippedWithoutStart ensures a zero start time adds no
// end-to-end sample while still counting the delivery.
func TestTriggerLatencySkippedWithoutStart(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()

	m := newDeliveryMetrics()
	svc := &Service{cfg: Config{DeliveryTimeout: 2 * time.Second}, metrics: m}
	svc.deliver(context.Background(), Message{
		Channel: KindWebhook, Target: ok.URL, Severity: "MINOR",
	})

	if got := counterValue(t, m.deliveries.WithLabelValues(KindWebhook, "MINOR", resultSuccess)); got != 1 {
		t.Fatalf("success counter = %v, want 1", got)
	}
	if got := histCount(t, m.triggerToDeliver.WithLabelValues(KindWebhook, "MINOR")); got != 0 {
		t.Fatalf("trigger->deliver samples with no start = %d, want 0", got)
	}
}

// TestRegisterMetrics verifies every instrument registers once.
func TestRegisterMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newDeliveryMetrics()
	if err := m.register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := m.register(reg); err == nil {
		t.Fatal("duplicate registration must error")
	}
}
