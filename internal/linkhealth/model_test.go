// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package linkhealth

import (
	"testing"
	"time"
)

// fixed tuning keeps test arithmetic explicit and fast.
func testTuning() Tuning {
	return Tuning{
		HeartbeatInterval: time.Second,
		SuspectAfter:      2 * time.Second,
		DownAfter:         3 * time.Second,
		SweepInterval:     time.Second,
		FlapWindow:        10 * time.Second,
		FlapThreshold:     5,
	}
}

func TestConnectThenHeartbeatStaysHealthy(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	if tr, ok := d.Connect("t", "g", t0); !ok || tr.To != StateHealthy {
		t.Fatalf("connect should yield HEALTHY, got %+v ok=%v", tr, ok)
	}
	// A duplicate connect is a no-op.
	if _, ok := d.Connect("t", "g", t0); ok {
		t.Fatal("duplicate connect must not transition")
	}
	// Timely heartbeat: no transition.
	if _, ok := d.Heartbeat("t", "g", t0.Add(time.Second)); ok {
		t.Fatal("timely heartbeat must not transition")
	}
	if got := d.State("t", "g"); got != StateHealthy {
		t.Fatalf("state = %v, want HEALTHY", got)
	}
}

func TestMissOneBeatGoesSuspectThenRecovers(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	d.Connect("t", "g", t0)

	trs := d.Evaluate(t0.Add(2 * time.Second))
	if len(trs) != 1 || trs[0].To != StateSuspect || trs[0].Reason != ReasonMissedBeat {
		t.Fatalf("expected one SUSPECT transition, got %+v", trs)
	}
	// A late-but-before-down heartbeat returns to HEALTHY without a DOWN.
	if tr, ok := d.Heartbeat("t", "g", t0.Add(2500*time.Millisecond)); ok {
		t.Fatalf("recovery from SUSPECT should not emit a DOWN transition, got %+v", tr)
	}
	if got := d.State("t", "g"); got != StateHealthy {
		t.Fatalf("state = %v, want HEALTHY", got)
	}
}

func TestMissThreeBeatsGoesDown(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	d.Connect("t", "g", t0)

	d.Evaluate(t0.Add(2 * time.Second)) // SUSPECT
	trs := d.Evaluate(t0.Add(3 * time.Second))
	if len(trs) != 1 || trs[0].To != StateDown || trs[0].Reason != ReasonLeaseExpired {
		t.Fatalf("expected lease-expired DOWN, got %+v", trs)
	}
	// A heartbeat after DOWN emits a recovery transition.
	tr, ok := d.Heartbeat("t", "g", t0.Add(5*time.Second))
	if !ok || tr.To != StateHealthy || tr.From != StateDown {
		t.Fatalf("expected DOWN->HEALTHY recovery, got %+v ok=%v", tr, ok)
	}
}

func TestPlannedGoodbyeDoesNotCountFlap(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	d.Connect("t", "g", t0)
	tr, ok := d.Goodbye("t", "g", Goodbye{Reason: "upgrade", Planned: true}, t0.Add(time.Second))
	if !ok || tr.To != StateDown || tr.Reason != ReasonPlannedDown || tr.Flapping {
		t.Fatalf("expected planned DOWN with no flap, got %+v ok=%v", tr, ok)
	}
	// A later LWT/transport close must not re-enter DOWN.
	if _, ok := d.TransportClosed("t", "g", t0.Add(2*time.Second)); ok {
		t.Fatal("transport close after planned DOWN must be a no-op")
	}
}

func TestTransportDownCountsFlap(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	// Five crash/reconnect cycles inside the flap window.
	for i := 0; i < 5; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		d.Connect("t", "g", at)
		tr, ok := d.TransportClosed("t", "g", at.Add(500*time.Millisecond))
		if !ok {
			t.Fatalf("cycle %d: expected transport DOWN", i)
		}
		if i == 4 {
			if !tr.Flapping {
				t.Fatalf("cycle %d: expected flapping flag on threshold crossing, got %+v", i, tr)
			}
		} else if tr.Flapping {
			t.Fatalf("cycle %d: flapping flag must not fire before threshold", i)
		}
	}
}

func TestFlapWindowExpires(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	for i := 0; i < 4; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		d.Connect("t", "g", at)
		d.TransportClosed("t", "g", at.Add(500*time.Millisecond))
	}
	// Fifth crash happens after the window slid past the earliest episodes,
	// so only the in-window episodes count and it must not flap.
	later := t0.Add(20 * time.Second)
	d.Connect("t", "g", later)
	tr, ok := d.TransportClosed("t", "g", later.Add(time.Second))
	if !ok {
		t.Fatal("expected transport DOWN")
	}
	if tr.Flapping {
		t.Fatal("aged-out episodes must not count toward flap")
	}
}

func TestUnknownGoodbyeReasonTreatedAsCrash(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(100, 0)
	d.Connect("t", "g", t0)
	tr, _ := d.Goodbye("t", "g", Goodbye{Reason: "weird"}, t0.Add(time.Second))
	if tr.Reason != ReasonTransportDown {
		t.Fatalf("unknown goodbye reason should be an ungraceful down, got %q", tr.Reason)
	}
}

func TestSplitDKey(t *testing.T) {
	tenant, gw := splitDKey("abc.def")
	if tenant != "abc" || gw != "def" {
		t.Fatalf("got %q %q", tenant, gw)
	}
	if _, gw := splitDKey("nodot"); gw != "" {
		t.Fatal("key without dot must yield empty gateway")
	}
}
