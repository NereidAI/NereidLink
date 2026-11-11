// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package linkhealth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

// recPublisher captures published advisories.
type recPublisher struct {
	msgs []struct {
		subject string
		data    []byte
	}
}

func (p *recPublisher) Publish(subject string, data []byte) error {
	p.msgs = append(p.msgs, struct {
		subject string
		data    []byte
	}{subject, data})
	return nil
}

// recCascade records offline derivations.
type recCascade struct{ calls []string }

func (c *recCascade) DeriveGatewayOffline(_ context.Context, _, gatewayID, _ string) (int, error) {
	c.calls = append(c.calls, gatewayID)
	return 1, nil
}

func quietMonitor(t *testing.T, tune Tuning) (*Monitor, *recPublisher) {
	t.Helper()
	pub := &recPublisher{}
	m := NewMonitor(nil, tune, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.nc = pub
	return m, pub
}

func TestMonitorPublishesSuspectDownRecovered(t *testing.T) {
	tune := testTuning()
	m, pub := quietMonitor(t, tune)
	ctx := context.Background()
	t0 := time.Unix(1000, 0)

	m.ObserveConnect(ctx, "t", "g", t0)
	for _, tr := range m.detector.Evaluate(t0.Add(2 * time.Second)) { // SUSPECT
		m.publishTransition(ctx, tr)
	}
	for _, tr := range m.detector.Evaluate(t0.Add(3 * time.Second)) { // DOWN
		m.publishTransition(ctx, tr)
	}
	m.ObserveHeartbeat(ctx, "t", "g", t0.Add(5*time.Second)) // recovered

	kinds := make([]string, len(pub.msgs))
	for i, msg := range pub.msgs {
		var adv Advisory
		if err := json.Unmarshal(msg.data, &adv); err != nil {
			t.Fatal(err)
		}
		kinds[i] = adv.Kind
		if i == 0 && msg.subject != "nl.linkhealth.t.g" {
			t.Fatalf("unexpected subject %q", msg.subject)
		}
	}
	want := []string{KindSuspect, KindDown, KindRecovered}
	if len(kinds) != 3 || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}

func TestMonitorPlannedGoodbyeKind(t *testing.T) {
	m, pub := quietMonitor(t, testTuning())
	ctx := context.Background()
	t0 := time.Unix(1000, 0)
	m.ObserveConnect(ctx, "t", "g", t0)
	m.ObserveGoodbye(ctx, "t", "g", true, "upgrade", t0.Add(time.Second))

	if len(pub.msgs) != 1 {
		t.Fatalf("expected one advisory, got %d", len(pub.msgs))
	}
	var adv Advisory
	if err := json.Unmarshal(pub.msgs[0].data, &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Kind != KindPlanned {
		t.Fatalf("kind = %q, want %q", adv.Kind, KindPlanned)
	}
}

func TestMonitorLeaseDownCascadesPresence(t *testing.T) {
	tune := testTuning()
	m, _ := quietMonitor(t, tune)
	cascade := &recCascade{}
	m.cascade = cascade
	ctx := context.Background()
	t0 := time.Unix(1000, 0)

	m.ObserveConnect(ctx, "t", "g", t0)
	// SUSPECT then DOWN purely from silence while transport is "up".
	m.detector.Evaluate(t0.Add(2 * time.Second))
	for _, tr := range m.detector.Evaluate(t0.Add(3 * time.Second)) {
		m.publishTransition(ctx, tr)
	}
	if len(cascade.calls) != 1 || cascade.calls[0] != "g" {
		t.Fatalf("expected one presence cascade for g, got %v", cascade.calls)
	}

	// A transport close after lease DOWN must not cascade again.
	cascade.calls = nil
	m.ObserveTransportClosed(ctx, "t", "g", t0.Add(4*time.Second))
	if len(cascade.calls) != 0 {
		t.Fatalf("transport close after lease DOWN must not re-cascade, got %v", cascade.calls)
	}
}

func TestMonitorFlappingKind(t *testing.T) {
	m, pub := quietMonitor(t, testTuning())
	ctx := context.Background()
	t0 := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		m.ObserveConnect(ctx, "t", "g", at)
		m.ObserveTransportClosed(ctx, "t", "g", at.Add(500*time.Millisecond))
	}
	last := pub.msgs[len(pub.msgs)-1]
	var adv Advisory
	if err := json.Unmarshal(last.data, &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Kind != KindFlapping {
		t.Fatalf("last advisory kind = %q, want %q", adv.Kind, KindFlapping)
	}
}
