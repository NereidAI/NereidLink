// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metrics.go defines the Prometheus instrumentation used to tune a
// compression level: frame counts by outcome, bytes before/after (for
// ratio), achieved-ratio gauge and encode/decode latency histograms.

package buscodec

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics bundles the bus-compression instruments.
type metrics struct {
	frames     *prometheus.CounterVec
	bytesIn    prometheus.Counter
	bytesOut   prometheus.Counter
	ratio      prometheus.Gauge
	encodeTime prometheus.Histogram
	decodeTime prometheus.Histogram
}

// newMetrics constructs unregistered instruments. They remain usable
// without registration so a Codec built outside a metrics registry
// (tests, one-off tools) still functions.
func newMetrics() *metrics {
	return &metrics{
		frames: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_frames_total",
			Help:      "Bus frames handled by compression outcome.",
		}, []string{"result"}),
		bytesIn: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_bytes_in_total",
			Help:      "Total input bytes offered to the bus encoder.",
		}),
		bytesOut: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_bytes_out_total",
			Help:      "Total bytes actually written after encoding.",
		}),
		ratio: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_ratio",
			Help:      "Achieved out/in size ratio of the last encoded frame (lower is better).",
		}),
		encodeTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_encode_seconds",
			Help:      "Time spent encoding one bus frame.",
			Buckets:   prometheus.ExponentialBuckets(0.00001, 2, 12),
		}),
		decodeTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nereidlink",
			Name:      "bus_compression_decode_seconds",
			Help:      "Time spent decoding one bus frame.",
			Buckets:   prometheus.ExponentialBuckets(0.00001, 2, 12),
		}),
	}
}

// register publishes every instrument onto reg.
func (m *metrics) register(reg prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		m.frames, m.bytesIn, m.bytesOut, m.ratio,
		m.encodeTime, m.decodeTime,
	}
	for _, collector := range collectors {
		if err := reg.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// observe records one encode outcome.
func (m *metrics) observe(result string, in, out int, started time.Time) {
	m.frames.WithLabelValues(result).Inc()
	m.bytesIn.Add(float64(in))
	m.bytesOut.Add(float64(out))
	if in > 0 && result == resultEncoded {
		m.ratio.Set(float64(out) / float64(in))
	}
	m.encodeTime.Observe(time.Since(started).Seconds())
}

// observeDecode records one decode duration.
func (m *metrics) observeDecode(d time.Duration) {
	m.decodeTime.Observe(d.Seconds())
}
