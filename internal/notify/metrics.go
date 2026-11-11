// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metrics.go instruments the notification delivery path (ADR-0019). It
// records per-channel delivery counts by result, the latency of one
// channel call (deliver->ack) and the end-to-end trigger->deliver
// latency. The two histograms expose p50/p95/p99 through
// histogram_quantile; a gauge could never answer a percentile question.

package notify

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// result labels for the delivery counter.
const (
	resultSuccess = "success"
	resultFailed  = "failed"
)

// deliveryMetrics bundles the notification instruments.
type deliveryMetrics struct {
	deliveries *prometheus.CounterVec
	// channelCall times a single webhook/SMTP invocation.
	channelCall *prometheus.HistogramVec
	// triggerToDeliver spans alarm start -> delivery completion.
	triggerToDeliver *prometheus.HistogramVec
}

// newDeliveryMetrics constructs unregistered instruments. They stay
// usable without registration so a Service built outside a metrics
// registry (tests, tools) still functions.
func newDeliveryMetrics() *deliveryMetrics {
	// Latency buckets span sub-millisecond local webhooks up to a
	// ~30s remote SMTP/HTTP timeout, covering the configured
	// DeliveryTimeout range.
	latencyBuckets := prometheus.ExponentialBuckets(0.001, 2, 15)
	return &deliveryMetrics{
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "notify_deliveries_total",
			Help:      "Notification deliveries by channel, severity and result.",
		}, []string{"channel", "severity", "result"}),
		channelCall: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "notify_delivery_seconds",
			Help:      "Time spent in one notification channel call.",
			Buckets:   latencyBuckets,
		}, []string{"channel", "severity"}),
		triggerToDeliver: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "notify_trigger_to_delivery_seconds",
			Help:      "End-to-end time from alarm start to delivery completion.",
			Buckets:   latencyBuckets,
		}, []string{"channel", "severity"}),
	}
}

// register publishes every instrument onto reg.
func (m *deliveryMetrics) register(reg prometheus.Registerer) error {
	for _, collector := range []prometheus.Collector{
		m.deliveries, m.channelCall, m.triggerToDeliver,
	} {
		if err := reg.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// observe records one delivery outcome, the channel call duration and,
// when the alarm start time is known, the end-to-end latency.
func (m *deliveryMetrics) observe(msg Message, callDuration time.Duration, failed bool) {
	result := resultSuccess
	if failed {
		result = resultFailed
	}
	m.deliveries.WithLabelValues(msg.Channel, severityLabel(msg.Severity), result).Inc()
	m.channelCall.WithLabelValues(msg.Channel, severityLabel(msg.Severity)).
		Observe(callDuration.Seconds())
	if !msg.AlarmStartedAt.IsZero() {
		m.triggerToDeliver.WithLabelValues(msg.Channel, severityLabel(msg.Severity)).
			Observe(time.Since(msg.AlarmStartedAt).Seconds())
	}
}

// severityLabel normalizes an empty severity so the label set stays
// bounded.
func severityLabel(severity string) string {
	if severity == "" {
		return "UNKNOWN"
	}
	return severity
}
