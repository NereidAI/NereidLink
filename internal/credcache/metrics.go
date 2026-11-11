// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// metrics.go instruments the credential cache for capacity planning.

package credcache

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds the cache counters.
type Metrics struct {
	// hits counts positive entries verified locally.
	hits prometheus.Counter
	// misses counts lookups that reached the fallback.
	misses prometheus.Counter
	// negativeHits counts entries served from the negative cache.
	negativeHits prometheus.Counter
	// evictions count explicit entry removals.
	evictions prometheus.Counter
}

// NewMetrics registers the counters with registerer; a nil registerer
// produces counters that can be registered later.
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	m := &Metrics{
		hits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Subsystem: "credcache",
			Name:      "hits_total",
			Help:      "Total credential authentications served from a positive cache entry.",
		}),
		misses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Subsystem: "credcache",
			Name:      "misses_total",
			Help:      "Total credential authentications that reached the registry fallback.",
		}),
		negativeHits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Subsystem: "credcache",
			Name:      "negative_hits_total",
			Help:      "Total connection attempts refused from the negative cache.",
		}),
		evictions: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nereidlink",
			Subsystem: "credcache",
			Name:      "evictions_total",
			Help:      "Total explicit credential cache evictions.",
		}),
	}
	if registerer != nil {
		registerer.MustRegister(m)
	}
	return m
}

// Describe implements prometheus.Collector, delegating to the four counters
// so the cache registers as one collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	m.hits.Describe(ch)
	m.misses.Describe(ch)
	m.negativeHits.Describe(ch)
	m.evictions.Describe(ch)
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.hits.Collect(ch)
	m.misses.Collect(ch)
	m.negativeHits.Collect(ch)
	m.evictions.Collect(ch)
}
