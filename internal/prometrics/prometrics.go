// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package prometrics exposes NereidLink metrics in Prometheus exposition
// format at /metrics.
//
// A custom Collector gathers fresh infrastructure values at every scrape
// by reusing stats.Service (Go runtime, PostgreSQL, NATS, JetStream,
// embedded broker and per-tenant gauges), so no background polling
// goroutine is needed. Managed CounterVec/HistogramVec instruments REST
// requests through the Instrument middleware. The default Go and process
// collectors are registered as well.
package prometrics

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/nereidai/nereidlink/internal/stats"
)

// Source is the stats surface the collector scrapes. stats.Service
// satisfies it.
type Source interface {
	// System returns the global infrastructure snapshot.
	System(ctx context.Context) (stats.SystemSnapshot, error)

	// Current returns per-tenant point-in-time gauges.
	Current(ctx context.Context) ([]stats.TenantGauges, error)
}

// Metrics owns the registry, custom collector and REST instrumentation.
type Metrics struct {
	// registry serves the exposition handler.
	registry *prometheus.Registry

	// httpRequests counts REST requests by method/route/code.
	httpRequests *prometheus.CounterVec

	// httpDuration records REST latency by method/route/code.
	httpDuration *prometheus.HistogramVec

	// inFlight tracks currently served REST requests.
	inFlight prometheus.Gauge
}

// New constructs and registers the metrics. startedAt seeds the uptime
// gauge inside the custom collector. liveClients, when non-nil, supplies the
// in-process WebSocket client count at each scrape; pass nil in processes that
// do not host the gateway. queueWatermarks, when non-nil, supplies bounded
// in-process queue occupancy (presence persistence, notification pools) for
// the ADR-0025 capacity watermark gauges; pass nil in processes without them.
func New(source Source, startedAt time.Time, liveClients func() int64, queueWatermarks func() []stats.QueueWatermark) (*Metrics, error) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newNereidCollector(source, startedAt, liveClients, queueWatermarks),
	)

	m := &Metrics{
		registry: registry,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "http_requests_total",
			Help:      "Total number of REST requests by method, route and status code.",
		}, []string{"method", "route", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "http_request_duration_seconds",
			Help:      "REST request latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "code"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nereidlink",
			Name:      "http_requests_in_flight",
			Help:      "Number of REST requests currently being served.",
		}),
	}
	registry.MustRegister(m.httpRequests, m.httpDuration, m.inFlight)
	return m, nil
}

// Handler returns the Prometheus exposition HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry exposes the underlying Prometheus registry.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// Instrument wraps an HTTP handler with REST request metrics. Path
// segments are normalized to bound label cardinality (UUIDs and long
// numeric segments become :id).
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inFlight.Inc()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		m.inFlight.Dec()

		route := normalizeRoute(r.URL.Path)
		labels := prometheus.Labels{
			"method": r.Method,
			"route":  route,
			"code":   strconv.Itoa(recorder.status),
		}
		m.httpRequests.With(labels).Inc()
		m.httpDuration.With(labels).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder captures the response status for metric labels.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap exposes the wrapped writer so handlers can walk the middleware
// chain (used to reach the trace writer for the error envelope).
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// WriteHeader records the status before delegating.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush supports streaming handlers (the response may wrap an
// http.Flusher, e.g. proxied responses).
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack delegates connection hijacking, required for the WebSocket
// upgrade. Without this method the metrics wrapper hides the upstream
// http.Hijacker and websocket.Accept replies 501. A successful hijack is
// recorded as 101 for the status code label.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	conn, buffer, err := hijacker.Hijack()
	if err == nil {
		r.status = http.StatusSwitchingProtocols
	}
	return conn, buffer, err
}

// fail logs a collection failure. The scrape still succeeds; stale
// descriptors produce no samples rather than breaking the endpoint.
func collectFailed(stage string, err error) {
	slog.Warn("prometheus collection failed", "stage", stage, "error", err)
}
