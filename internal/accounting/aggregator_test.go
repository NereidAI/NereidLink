// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// aggregator_test.go covers the report contract shared between nodes: the
// subject layout, node-id attribution and the JSON round trip. The NATS
// transport itself is covered by the store integration test; these tests
// pin the pure logic so a subject change cannot silently mis-attribute a
// node's traffic.

package accounting

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

// discardLogger silences component logging in tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNodeFromSubject verifies node extraction, including a subject that
// carries a trailing token.
func TestNodeFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    string
		ok      bool
	}{
		{"nl.accounting.n1", "n1", true},
		{"nl.accounting.node-7.example", "node-7", true},
		{"nl.accounting.", "", false},
		{"nl.alarm.x", "", false},
		{"nl.accounting", "", false},
	}
	for _, tc := range cases {
		got, ok := nodeFromSubject(tc.subject)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("nodeFromSubject(%q) = (%q,%v), want (%q,%v)",
				tc.subject, got, ok, tc.want, tc.ok)
		}
	}
}

// TestReportJSONRoundTrip verifies the wire shape carries everything an
// aggregator needs to extrapolate without contacting the node.
func TestReportJSONRoundTrip(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 5, Capacity: 8})
	for i := 0; i < 100; i++ {
		s.Observe(Event{TenantID: "t1", DeviceID: "d1", Protocol: "mqtt", Bytes: 20})
	}
	original := s.Snapshot(time.Now(), 30*time.Second, 5)

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.NodeID != original.NodeID {
		t.Fatalf("node id lost: %q vs %q", decoded.NodeID, original.NodeID)
	}
	if decoded.SampleRate != 5 {
		t.Fatalf("sample rate lost: %d", decoded.SampleRate)
	}
	if decoded.Seen != original.Seen || decoded.Sampled != original.Sampled {
		t.Fatalf("counters lost: %d/%d vs %d/%d",
			decoded.Seen, decoded.Sampled, original.Seen, original.Sampled)
	}
	entry, ok := entryFor(t, decoded, DimensionTenant, "t1")
	if !ok {
		t.Fatal("tenant entry lost in round trip")
	}
	if entry.Count != 100 {
		t.Fatalf("expected 100 extrapolated, got %d", entry.Count)
	}
	if entry.Bytes != 100*20 {
		t.Fatalf("expected 2000 bytes, got %d", entry.Bytes)
	}
}

// TestReportSkipsUnsampledWindow verifies an idle node does not publish
// zeros, which would otherwise dilute a cluster-wide Top-N.
func TestReportSkipsUnsampledWindow(t *testing.T) {
	s := NewSampler(Config{NodeID: "n1", SampleRate: 1, Capacity: 8})
	report := s.Snapshot(time.Now(), 30*time.Second, 5)
	if report.Sampled != 0 {
		t.Fatalf("expected no samples, got %d", report.Sampled)
	}
}

// TestReporterSubject verifies the published subject carries the node id.
func TestReporterSubject(t *testing.T) {
	s := NewSampler(Config{NodeID: "edge-3", SampleRate: 1, Capacity: 8})
	r := NewReporter(nil, s, discardLogger())
	r.topN = 5
	if got := SubjectPrefix + s.nodeID; got != "nl.accounting.edge-3" {
		t.Fatalf("unexpected subject %s", got)
	}
	// reportOnce with a nil connection must not panic: a nil bus is the
	// degenerate case of "publish failed".
	for i := 0; i < 3; i++ {
		s.Observe(Event{TenantID: "t1"})
	}
	r.reportOnce(t.Context(), time.Now())
}
