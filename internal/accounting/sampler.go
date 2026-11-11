// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sampler.go implements the ADR-0025 hot-path accounting sampler: a 1/N
// sampler that keeps a bounded local heavy-hitter view per dimension and
// periodically publishes it for cluster-wide aggregation.
//
// Design constraints (ADR-0025 "low-overhead granular metrics"):
//
//   - The ingest hot path only touches preallocated structures. Observe
//     must not allocate per event beyond what the sketch itself needs,
//     must not build label strings, and must not do any I/O.
//   - Memory is bounded by (dimensions x capacity), never by traffic.
//   - Accounting is lowest priority: sampling drops frames instead of
//     slowing ingest, and a failed publish never affects the caller.
//
// Sampling is deterministic (every Nth frame) rather than random so that
// the estimate is unbiased and reproducible: a debug replay of the same
// traffic yields the same accounting view.

package accounting

import (
	"strings"
	"sync/atomic"
	"time"
)

// Dimension names the accounting dimension a value belongs to. The set
// is fixed and validated at construction so a typo cannot silently create
// an unbounded new series.
type Dimension string

// The supported accounting dimensions. These mirror the sFlow-style
// breakdown in ADR-0025: who sent it, from where, over what, on which
// node and on which subject pattern.
const (
	// DimensionTenant attributes traffic to a tenant.
	DimensionTenant Dimension = "tenant"
	// DimensionDevice attributes traffic to a device.
	DimensionDevice Dimension = "device"
	// DimensionIP attributes traffic to a source address.
	DimensionIP Dimension = "ip"
	// DimensionProtocol attributes traffic to an ingress protocol.
	DimensionProtocol Dimension = "protocol"
	// DimensionNode attributes traffic to the serving node.
	DimensionNode Dimension = "node"
	// DimensionTopic attributes traffic to a normalized subject pattern.
	DimensionTopic Dimension = "topic"
)

// dimensions is the canonical ordered dimension list.
var dimensions = []Dimension{
	DimensionTenant,
	DimensionDevice,
	DimensionIP,
	DimensionProtocol,
	DimensionNode,
	DimensionTopic,
}

// Dimensions returns the supported accounting dimensions.
func Dimensions() []Dimension {
	out := make([]Dimension, len(dimensions))
	copy(out, dimensions)
	return out
}

// ParseDimension validates a dimension name.
func ParseDimension(value string) (Dimension, bool) {
	for _, d := range dimensions {
		if string(d) == value {
			return d, true
		}
	}
	return "", false
}

// wildcardSegment replaces a variable topic segment so subjects collapse
// into a bounded set of patterns instead of one series per device topic.
const wildcardSegment = "{id}"

// NormalizeTopic collapses a concrete subject into a pattern by replacing
// UUID-like and long numeric segments with a placeholder. Subjects are the
// highest-cardinality dimension; without normalization a fleet of devices
// would evict every useful candidate from the bounded sketch.
//
// The separator is detected rather than assumed, because the two ingress
// planes use different grammars for the same purpose: MQTT topics are slash
// separated (mqttapi.TopicDeviceTelemetry and friends) while NATS subjects
// are dot separated. A slash wins when both appear, since that is a
// device-scoped subject such as "nl.ingest.devices/<uuid>" whose dynamic
// segment still has to collapse. Collapsing only one grammar would leave the
// other plane's per-device topics unnormalized, so any future per-device
// topic would silently reintroduce unbounded cardinality.
func NormalizeTopic(subject string) string {
	if subject == "" {
		return ""
	}
	sep := "."
	if strings.Contains(subject, "/") {
		sep = "/"
	}
	parts := strings.Split(subject, sep)
	replaced := false
	for i, part := range parts {
		if isVariableSegment(part) {
			parts[i] = wildcardSegment
			replaced = true
		}
	}
	// Avoid re-joining when nothing changed: it keeps the frozen platform
	// topics on an allocation-free path.
	if !replaced {
		return subject
	}
	return strings.Join(parts, sep)
}

// isVariableSegment reports whether a subject segment looks like a
// per-entity identifier rather than a stable routing token. Platform
// subjects embed tenant UUIDs, so canonical UUID form must be recognised
// structurally (hex groups separated by dashes) rather than by a plain hex
// test, which a dashed UUID never passes.
func isVariableSegment(segment string) bool {
	if isUUID(segment) {
		return true
	}
	// A purely numeric segment is an identifier when it is long enough to
	// be a device/sequence number; short numeric segments (shard ids,
	// partition numbers) stay literal so their distribution remains
	// visible in the accounting view.
	return len(segment) >= 6 && isNumeric(segment)
}

// isUUID reports whether s has the canonical 8-4-4-4-12 hexadecimal form.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHexRune := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			if !isHexRune {
				return false
			}
		}
	}
	return true
}

// isNumeric reports whether every rune is a digit.
func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Observer records accounted frames. The ingress planes depend on this
// interface rather than on *Sampler so that accounting stays a pluggable,
// lowest-priority concern: a plane compiled without accounting wired holds
// a nil Observer and skips the call entirely.
type Observer interface {
	// Observe accounts one frame.
	Observe(evt Event)
}

// Event is one accounted frame. Empty dimension fields are ignored so a
// caller only has to supply what it actually knows (an ingress that never
// sees a remote IP simply leaves it empty).
type Event struct {
	// TenantID attributes the frame to a tenant.
	TenantID string
	// DeviceID attributes the frame to a device.
	DeviceID string
	// RemoteIP attributes the frame to a source address.
	RemoteIP string
	// Protocol attributes the frame to an ingress (mqtt/http).
	Protocol string
	// NodeID attributes the frame to the serving node.
	NodeID string
	// Topic is the raw subject; it is normalized before accounting.
	Topic string
	// Bytes is the frame size in bytes.
	Bytes int
}

// Config configures a Sampler.
type Config struct {
	// NodeID identifies the reporting node.
	NodeID string

	// SampleRate is the 1/N divisor: one frame in N is recorded. Values
	// below 1 are treated as 1 (account everything).
	SampleRate int

	// Capacity is the per-dimension heavy-hitter candidate budget.
	Capacity int
}

// Sampler records a 1/N sample of the ingest stream per dimension.
type Sampler struct {
	nodeID string
	// sampleRate is the 1/N divisor used for the sampling decision.
	sampleRate uint64
	// scaleRate is the same divisor as a signed value. Extrapolating a
	// byte or count estimate multiplies by it, and that arithmetic must be
	// signed: multiplying unsigned values risks wrapping a huge estimate
	// into a small one.
	scaleRate int64

	// seen counts every frame offered to the sampler, sampled or not. It
	// is the denominator that makes the sampled counts extrapolatable.
	seen atomic.Uint64
	// sampled counts the frames that were actually recorded.
	sampled atomic.Uint64

	// sketches holds one heavy-hitter structure per dimension.
	sketches map[Dimension]*Sketch
}

// NewSampler builds a sampler from config.
func NewSampler(cfg Config) *Sampler {
	s := &Sampler{
		nodeID:     cfg.NodeID,
		sampleRate: uint64(max(cfg.SampleRate, 1)),
		sketches:   make(map[Dimension]*Sketch, len(dimensions)),
	}
	s.scaleRate = int64(max(cfg.SampleRate, 1))
	for _, d := range dimensions {
		s.sketches[d] = NewSketch(cfg.Capacity)
	}
	return s
}

// SampleRate returns the active 1/N divisor.
func (s *Sampler) SampleRate() uint64 {
	return s.sampleRate
}

// Seen returns the total number of frames offered to the sampler.
func (s *Sampler) Seen() uint64 {
	return s.seen.Load()
}

// Observe accounts one frame. It is safe for concurrent use, allocates no
// per-event memory beyond the sketch's own candidate slots, and never
// blocks on I/O. A frame is recorded when it lands on the sampling
// boundary; the boundary is derived from the total frame count so the
// decision needs no random source.
func (s *Sampler) Observe(evt Event) {
	total := s.seen.Add(1)
	if total%s.sampleRate != 0 {
		return
	}
	s.sampled.Add(1)

	// A sampled frame stands in for sampleRate frames, so its payload size
	// is scaled up to keep byte estimates comparable with the counts.
	weight := int64(0)
	if evt.Bytes > 0 {
		weight = saturateMul(int64(evt.Bytes), s.scaleRate)
	}

	s.observeDimension(DimensionTenant, evt.TenantID, weight)
	s.observeDimension(DimensionDevice, evt.DeviceID, weight)
	s.observeDimension(DimensionIP, evt.RemoteIP, weight)
	s.observeDimension(DimensionProtocol, evt.Protocol, weight)
	s.observeDimension(DimensionNode, firstNonEmpty(evt.NodeID, s.nodeID), weight)
	s.observeDimension(DimensionTopic, NormalizeTopic(evt.Topic), weight)
}

// observeDimension records value in one dimension. Empty values are
// ignored: an ingress that cannot supply a dimension must not create a
// single giant "" candidate that masks every real value.
func (s *Sampler) observeDimension(d Dimension, value string, weight int64) {
	if value == "" {
		return
	}
	s.sketches[d].ObserveWeighted(value, weight)
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// DimensionEntry is one accounted dimension value in a report.
type DimensionEntry struct {
	// Value is the dimension value (already normalized for topics).
	Value string
	// Count is the scaled estimate of occurrences in the window.
	Count uint64
	// Bytes is the scaled estimate of bytes in the window.
	Bytes int64
	// Tracked reports whether the value still holds a candidate slot.
	// A false value means the count is a lower bound produced by decay.
	Tracked bool
}

// Report is one node's accounting window. It is the payload published to
// the cluster aggregator and is intentionally self-describing: the
// aggregator must be able to extrapolate the sample without asking the
// node.
type Report struct {
	// NodeID identifies the reporting node.
	NodeID string `json:"node_id"`
	// WindowStart is the inclusive start of the window.
	WindowStart time.Time `json:"window_start"`
	// WindowEnd is the exclusive end of the window.
	WindowEnd time.Time `json:"window_end"`
	// SampleRate is the 1/N divisor used during the window.
	SampleRate int64 `json:"sample_rate"`
	// Seen is the total number of frames offered during the window.
	Seen uint64 `json:"seen"`
	// Sampled is the number of frames actually recorded.
	Sampled uint64 `json:"sampled"`
	// Dimensions maps each dimension to its ranked entries.
	Dimensions map[string][]DimensionEntry `json:"dimensions"`
}

// Snapshot renders the current window as a report and ages the structures
// so the next window reflects only newer traffic. It is called by the
// reporter, never by the hot path.
func (s *Sampler) Snapshot(now time.Time, window time.Duration, topN int) Report {
	report := Report{
		NodeID:      s.nodeID,
		WindowStart: now.Add(-window),
		WindowEnd:   now,
		SampleRate:  s.scaleRate,
		Seen:        s.seen.Swap(0),
		Sampled:     s.sampled.Swap(0),
		Dimensions:  make(map[string][]DimensionEntry, len(dimensions)),
	}
	for d, sketch := range s.sketches {
		candidates := sketch.Top(topN)
		entries := make([]DimensionEntry, 0, len(candidates))
		for _, c := range candidates {
			entries = append(entries, DimensionEntry{
				Value:   c.Value,
				Count:   saturateMulCount(c.Count, s.scaleRate),
				Bytes:   c.Bytes,
				Tracked: true,
			})
		}
		report.Dimensions[string(d)] = entries
		// Reset (not decay) keeps consecutive report windows disjoint, which
		// is what lets the store sum them into an exact window total.
		sketch.Reset()
	}
	return report
}
