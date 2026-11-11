// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package linkhealth

import (
	"testing"
	"time"
)

// TestEdgeEpisodeFlapCrossing feeds five edge episodes (unmatched by a
// lease episode) into the independent edge flap window and asserts the
// crossing advisory fires exactly once, on the fifth episode.
func TestEdgeEpisodeFlapCrossing(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(1000, 0)
	for i := 0; i < 6; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		tr, ok := d.ObserveEdgeEpisode("t", "g", at)
		if i < 4 {
			if ok {
				t.Fatalf("episode %d: no flap advisory before threshold", i)
			}
			continue
		}
		if i == 4 {
			if !ok || !tr.EdgeFlap || tr.Source != SourceEdge {
				t.Fatalf("episode %d: expected edge flap crossing, got %+v ok=%v", i, tr, ok)
			}
		} else if ok {
			t.Fatalf("episode %d: a burst alerts once; no second advisory", i)
		}
	}
}

// TestEdgeEpisodeSettleAfterWindow asserts Evaluate emits the settle
// transition once the burst episodes age out of the window, and re-arms the
// detector for a following burst.
func TestEdgeEpisodeSettleAfterWindow(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		d.ObserveEdgeEpisode("t", "g", t0.Add(time.Duration(i)*time.Second))
	}

	// Nothing settles while episodes remain inside the window.
	if trs := d.Evaluate(t0.Add(9 * time.Second)); len(trs) != 0 {
		t.Fatalf("expected no settle inside window, got %+v", trs)
	}

	trs := d.Evaluate(t0.Add(16 * time.Second))
	if len(trs) != 1 || !trs[0].EdgeSettled || trs[0].Source != SourceEdge {
		t.Fatalf("expected one edge settle transition, got %+v", trs)
	}

	// After settle, a fresh burst must alert again.
	t1 := t0.Add(100 * time.Second)
	var crossed bool
	for i := 0; i < 5; i++ {
		if _, ok := d.ObserveEdgeEpisode("t", "g", t1.Add(time.Duration(i)*time.Second)); ok {
			crossed = true
		}
	}
	if !crossed {
		t.Fatal("detector must re-arm after settle")
	}
}

// TestEdgeEpisodeWindowExpires asserts episodes older than the window do not
// count toward a later crossing.
func TestEdgeEpisodeWindowExpires(t *testing.T) {
	d := NewDetector(testTuning())
	t0 := time.Unix(1000, 0)
	for i := 0; i < 4; i++ {
		d.ObserveEdgeEpisode("t", "g", t0.Add(time.Duration(i)*time.Second))
	}
	if _, ok := d.ObserveEdgeEpisode("t", "g", t0.Add(20*time.Second)); ok {
		t.Fatal("aged-out edge episodes must not count toward flap")
	}
}
