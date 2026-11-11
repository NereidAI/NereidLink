// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sampler_test.go covers the 1/N sampler: unbiased extrapolation,
// dimension isolation, empty-value handling, topic normalization and the
// reset-on-snapshot contract.

package accounting

import (
	"testing"
	"time"
)

// entryFor finds a dimension entry by value.
func entryFor(t *testing.T, report Report, dimension Dimension, value string) (DimensionEntry, bool) {
	t.Helper()
	for _, e := range report.Dimensions[string(dimension)] {
		if e.Value == value {
			return e, true
		}
	}
	return DimensionEntry{}, false
}

// TestSamplerOneInN verifies only every Nth frame is recorded but the
// reported count is scaled back up to the full traffic volume.
func TestSamplerOneInN(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 10, Capacity: 32})
	for i := 0; i < 1000; i++ {
		s.Observe(Event{TenantID: "t1", DeviceID: "d1", Protocol: "mqtt", Bytes: 100})
	}
	if got := s.Seen(); got != 1000 {
		t.Fatalf("expected 1000 seen, got %d", got)
	}
	report := s.Snapshot(time.Now(), time.Minute, 10)
	if report.Sampled != 100 {
		t.Fatalf("expected 100 sampled, got %d", report.Sampled)
	}
	if report.Seen != 1000 {
		t.Fatalf("expected report seen 1000, got %d", report.Seen)
	}
	e, ok := entryFor(t, report, DimensionTenant, "t1")
	if !ok {
		t.Fatal("tenant t1 missing from report")
	}
	// 100 samples * rate 10 = 1000 estimated frames.
	if e.Count != 1000 {
		t.Fatalf("expected extrapolated count 1000, got %d", e.Count)
	}
	if e.Bytes != 1000*100 {
		t.Fatalf("expected extrapolated bytes 100000, got %d", e.Bytes)
	}
}

// TestSamplerRateOne verifies rate 1 records everything.
func TestSamplerRateOne(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 0, Capacity: 8})
	for i := 0; i < 50; i++ {
		s.Observe(Event{TenantID: "t1"})
	}
	report := s.Snapshot(time.Now(), time.Minute, 5)
	if report.SampleRate != 1 {
		t.Fatalf("expected normalized rate 1, got %d", report.SampleRate)
	}
	e, _ := entryFor(t, report, DimensionTenant, "t1")
	if e.Count != 50 {
		t.Fatalf("expected count 50, got %d", e.Count)
	}
}

// TestSamplerDimensionsAreIndependent verifies each dimension tracks only
// its own values.
func TestSamplerDimensionsAreIndependent(t *testing.T) {
	s := NewSampler(Config{NodeID: "node-a", SampleRate: 1, Capacity: 16})
	s.Observe(Event{TenantID: "t1", DeviceID: "d1", RemoteIP: "10.0.0.1",
		Protocol: "mqtt", Topic: "sensors.abc.temp"})

	report := s.Snapshot(time.Now(), time.Minute, 10)
	if _, ok := entryFor(t, report, DimensionTenant, "t1"); !ok {
		t.Fatal("tenant missing")
	}
	if _, ok := entryFor(t, report, DimensionDevice, "d1"); !ok {
		t.Fatal("device missing")
	}
	if _, ok := entryFor(t, report, DimensionIP, "10.0.0.1"); !ok {
		t.Fatal("ip missing")
	}
	if _, ok := entryFor(t, report, DimensionProtocol, "mqtt"); !ok {
		t.Fatal("protocol missing")
	}
	// The node dimension falls back to the configured node id.
	if _, ok := entryFor(t, report, DimensionNode, "node-a"); !ok {
		t.Fatal("node missing")
	}
	if _, ok := entryFor(t, report, DimensionTenant, "d1"); ok {
		t.Fatal("tenant dimension leaked a device value")
	}
}

// TestSamplerIgnoresEmptyValues verifies a missing dimension does not
// create a single giant candidate that masks real values.
func TestSamplerIgnoresEmptyValues(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 1, Capacity: 16})
	s.Observe(Event{TenantID: "t1"})
	report := s.Snapshot(time.Now(), time.Minute, 10)
	if _, ok := entryFor(t, report, DimensionIP, ""); ok {
		t.Fatal("empty ip must not be accounted")
	}
	if _, ok := entryFor(t, report, DimensionDevice, ""); ok {
		t.Fatal("empty device must not be accounted")
	}
}

// TestSamplerNodeOverride verifies an explicit node id wins over config.
func TestSamplerNodeOverride(t *testing.T) {
	s := NewSampler(Config{NodeID: "cfg-node", SampleRate: 1, Capacity: 8})
	s.Observe(Event{TenantID: "t1", NodeID: "explicit"})
	report := s.Snapshot(time.Now(), time.Minute, 10)
	if _, ok := entryFor(t, report, DimensionNode, "explicit"); !ok {
		t.Fatal("explicit node id not used")
	}
}

// TestNormalizeTopic verifies subject cardinality is collapsed.
func TestNormalizeTopic(t *testing.T) {
	longID := "00000000-0000-0000-0000-000000000100"
	plain := "00000000-0000-0000-0000-000000000101"
	if got := NormalizeTopic("nl.telemetry.s1." + longID + ".temp"); got != "nl.telemetry.s1."+wildcardSegment+".temp" {
		t.Fatalf("uuid segment not collapsed: %s", got)
	}
	// Two different devices must collapse to the same pattern, otherwise
	// the topic dimension is as unbounded as the device dimension.
	a := NormalizeTopic("nl.telemetry.s1." + longID + ".temp")
	b := NormalizeTopic("nl.telemetry.s1." + plain + ".temp")
	if a != b {
		t.Fatalf("distinct device ids produced distinct patterns: %s vs %s", a, b)
	}
	if got := NormalizeTopic("nl.telemetry.s1"); got != "nl.telemetry.s1" {
		t.Fatalf("stable segments must stay literal: %s", got)
	}
	if got := NormalizeTopic("nl.telemetry.s1.123456.temp"); got != "nl.telemetry.s1."+wildcardSegment+".temp" {
		t.Fatalf("numeric identifier not collapsed: %s", got)
	}
	if got := NormalizeTopic(""); got != "" {
		t.Fatalf("empty subject should stay empty, got %s", got)
	}
	// The MQTT plane separates its topic grammar with slashes, so a
	// normalizer that only splits on dots would leave every per-device MQTT
	// topic uncollapsed. Both grammars must collapse.
	mqttA := NormalizeTopic("v1/devices/" + longID + "/telemetry")
	mqttB := NormalizeTopic("v1/devices/" + plain + "/telemetry")
	if want := "v1/devices/" + wildcardSegment + "/telemetry"; mqttA != want {
		t.Fatalf("MQTT uuid segment not collapsed: got %s want %s", mqttA, want)
	}
	if mqttA != mqttB {
		t.Fatalf("MQTT distinct device ids produced distinct patterns: %s vs %s", mqttA, mqttB)
	}
	// A device-scoped NATS subject carrying an MQTT-style path must still
	// collapse, because a slash anywhere in the subject selects the slash
	// grammar.
	if got := NormalizeTopic("nl.ingest.devices/" + longID + "/telemetry"); got !=
		"nl.ingest.devices/"+wildcardSegment+"/telemetry" {
		t.Fatalf("device-scoped subject not collapsed: %s", got)
	}
	// The frozen platform topics carry no dynamic segment and must pass
	// through byte for byte.
	if got := NormalizeTopic("v1/me/telemetry"); got != "v1/me/telemetry" {
		t.Fatalf("platform topic altered: %s", got)
	}
}

// TestSamplerSnapshotResets verifies a snapshot closes the window and
// ages the sketches so the next window is independent.
func TestSamplerSnapshotResets(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 1, Capacity: 16})
	for i := 0; i < 100; i++ {
		s.Observe(Event{TenantID: "t1"})
	}
	first := s.Snapshot(time.Now(), time.Minute, 10)
	if e, _ := entryFor(t, first, DimensionTenant, "t1"); e.Count != 100 {
		t.Fatalf("first window count %d", e.Count)
	}
	if first.WindowEnd.Before(first.WindowStart) {
		t.Fatal("window bounds are inverted")
	}
	// Second window with no traffic must not repeat the first window's
	// volume.
	second := s.Snapshot(time.Now().Add(time.Minute), time.Minute, 10)
	if second.Seen != 0 || second.Sampled != 0 {
		t.Fatalf("expected empty second window, got seen=%d sampled=%d",
			second.Sampled, second.Sampled)
	}
	e, _ := entryFor(t, second, DimensionTenant, "t1")
	if e.Count >= 100 {
		t.Fatalf("decayed count %d should be well below 100", e.Count)
	}
}

// TestSamplerTopNLimitsEntries verifies the report honours the caller's
// Top-N budget per dimension.
func TestSamplerTopNLimitsEntries(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 1, Capacity: 256})
	for i := 0; i < 200; i++ {
		s.Observe(Event{TenantID: "t" + string(rune('a'+i%26)) + itoa(i)})
	}
	report := s.Snapshot(time.Now(), time.Minute, 5)
	if got := len(report.Dimensions[string(DimensionTenant)]); got > 5 {
		t.Fatalf("expected at most 5 entries, got %d", got)
	}
}

// itoa avoids importing strconv for a single test helper.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestParseDimension verifies dimension validation.
func TestParseDimension(t *testing.T) {
	if _, ok := ParseDimension("tenant"); !ok {
		t.Fatal("tenant should parse")
	}
	if _, ok := ParseDimension("nonsense"); ok {
		t.Fatal("unknown dimension must be rejected")
	}
	if got := len(Dimensions()); got != 6 {
		t.Fatalf("expected 6 dimensions, got %d", got)
	}
}
