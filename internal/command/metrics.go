// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metrics.go defines command lifecycle metrics. The narrow interface lets
// the router use a no-op implementation when Prometheus is not wired.

package command

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// commandMetrics records command outcomes and end-to-end latency.
type commandMetrics interface {
	Observe(tenantID, status string, duration time.Duration)
}

// Metrics is the Prometheus implementation.
type Metrics struct {
	// total counts commands by tenant and terminal status.
	total *prometheus.CounterVec

	// duration records command latency by tenant and status.
	duration *prometheus.HistogramVec
}

// NewMetrics constructs and registers command metrics.
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "commands_total",
			Help:      "Total commands by tenant and terminal status.",
		}, []string{"tenant", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "command_duration_seconds",
			Help:      "End-to-end command duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"tenant", "status"}),
	}
	if err := registerer.Register(m.total); err != nil {
		return nil, err
	}
	if err := registerer.Register(m.duration); err != nil {
		return nil, err
	}
	return m, nil
}

// Observe records one terminal command.
func (m *Metrics) Observe(tenantID, status string, duration time.Duration) {
	labels := prometheus.Labels{"tenant": tenantID, "status": status}
	m.total.With(labels).Inc()
	m.duration.With(labels).Observe(duration.Seconds())
}

// noopMetrics ignores command metrics.
type noopMetrics struct{}

// Observe implements commandMetrics.
func (noopMetrics) Observe(string, string, time.Duration) {}
