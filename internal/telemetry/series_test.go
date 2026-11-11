// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// series_test.go pins the window-to-rollup routing decision.
//
// The routing rule is the one piece of the telemetry read path that is pure
// logic and therefore cheap to pin down. It matters because a wrong choice
// here is invisible in testing and obvious in production: pointing a
// sub-hour query at an hourly rollup returns silently coarser data, and
// pointing a coarse query at the hypertable returns the right answer while
// quietly scanning years of rows.

package telemetry

import (
	"testing"
	"time"
)

func TestSeriesSourceRoutesByBucketWidth(t *testing.T) {
	cases := []struct {
		name      string
		bucket    time.Duration
		wantTable string
		wantCol   string
	}{
		// Sub-hour buckets cannot come from either rollup, so the hypertable
		// is the only relation that holds the resolution.
		{"5s", 5 * time.Second, "ts_telemetry", "time"},
		{"30s", 30 * time.Second, "ts_telemetry", "time"},
		{"1m", time.Minute, "ts_telemetry", "time"},
		{"30m", 30 * time.Minute, "ts_telemetry", "time"},

		// Exactly at the hourly rollup width, and coarser than it but finer
		// than daily: the hourly rollup answers, re-bucketing as needed.
		{"1h", time.Hour, "ts_telemetry_hourly", "bucket"},
		{"3h", 3 * time.Hour, "ts_telemetry_hourly", "bucket"},
		{"12h", 12 * time.Hour, "ts_telemetry_hourly", "bucket"},

		// At and beyond the daily rollup width the daily rollup wins.
		{"24h", 24 * time.Hour, "ts_telemetry_daily", "bucket"},
		{"48h", 48 * time.Hour, "ts_telemetry_daily", "bucket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table, col := seriesSource(tc.bucket)
			if table != tc.wantTable || col != tc.wantCol {
				t.Fatalf("seriesSource(%s) = (%s, %s), want (%s, %s)",
					tc.bucket, table, col, tc.wantTable, tc.wantCol)
			}
		})
	}
}

// TestSeriesSourceNeverFinerThanRollup guards the invariant the query relies
// on: a rollup is only ever selected when its own width is no finer than the
// requested bucket. Violating it would drop detail silently.
func TestSeriesSourceNeverFinerThanRollup(t *testing.T) {
	widths := map[string]time.Duration{
		"ts_telemetry_hourly": time.Hour,
		"ts_telemetry_daily":  24 * time.Hour,
	}
	buckets := []time.Duration{
		5 * time.Second, time.Minute, 30 * time.Minute,
		time.Hour, 3 * time.Hour, 12 * time.Hour,
		24 * time.Hour, 72 * time.Hour,
	}
	for _, b := range buckets {
		table, _ := seriesSource(b)
		width, isRollup := widths[table]
		if isRollup && width > b {
			t.Fatalf("seriesSource(%s) chose rollup %s whose width %s is finer than the requested bucket",
				b, table, width)
		}
	}
}

// TestBucketIntervalStaysWithinChartCapacity protects the other half of the
// contract: whatever bucket is chosen must fit inside maxSeriesBuckets, so
// long windows cannot blow up the response size.
// newServerWithRollups returns a Server whose coverage probe has already
// resolved to ready/unready, without touching a database. Marking the sync.Once
// done directly is the only way to exercise the gate without a live pool.
func newServerWithRollups(ready bool) *Server {
	s := &Server{}
	s.rollupOnce.Do(func() {}) // pretend the one-time probe already ran
	s.rollupReady = ready
	return s
}

// TestSeriesRelationFallsBackWhenRollupsNotProven is the regression guard for
// L-021: a rollup that exists but was never backfilled must never be read,
// because it answers historical windows with zero rows.
func TestSeriesRelationFallsBackWhenRollupsNotProven(t *testing.T) {
	s := newServerWithRollups(false)
	for _, bucket := range []time.Duration{time.Hour, 24 * time.Hour, 72 * time.Hour} {
		table, col := s.seriesRelation(bucket)
		if table != "ts_telemetry" || col != "time" {
			t.Fatalf("seriesRelation(%s) with unready rollups = (%s, %s), want (ts_telemetry, time)",
				bucket, table, col)
		}
	}
}

// TestSeriesRelationUsesRollupsWhenProven is the other half of the gate. A
// guard that only ever returns false is "safe" and useless; this pins that it
// reports ready when coverage is proven. See L-024.
func TestSeriesRelationUsesRollupsWhenProven(t *testing.T) {
	s := newServerWithRollups(true)
	cases := map[time.Duration][2]string{
		30 * time.Minute: {"ts_telemetry", "time"},
		time.Hour:        {"ts_telemetry_hourly", "bucket"},
		24 * time.Hour:   {"ts_telemetry_daily", "bucket"},
	}
	for bucket, want := range cases {
		table, col := s.seriesRelation(bucket)
		if table != want[0] || col != want[1] {
			t.Fatalf("seriesRelation(%s) with ready rollups = (%s, %s), want (%s, %s)",
				bucket, table, col, want[0], want[1])
		}
	}
}

func TestBucketIntervalStaysWithinChartCapacity(t *testing.T) {
	windows := []time.Duration{
		5 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour,
		7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour,
	}
	for _, w := range windows {
		b := bucketInterval(w)
		if got := w / b; got > maxSeriesBuckets {
			t.Fatalf("bucketInterval(%s) = %s yielding %d buckets, exceeds maxSeriesBuckets %d",
				w, b, got, maxSeriesBuckets)
		}
		if b > w {
			t.Fatalf("bucketInterval(%s) = %s, which is longer than the window itself", w, b)
		}
	}
}

// TestRoutingDoesNotChangeResolution documents the deliberate divergence from
// ADR-0004's literal span thresholds. Routing on span rather than bucket width
// would coarsen existing charts, so the test asserts that a 7-day window --
// which ADR-0004 would route to the daily rollup -- still gets hourly
// resolution from the hourly rollup.
func TestRoutingDoesNotChangeResolution(t *testing.T) {
	window := 7 * 24 * time.Hour
	bucket := bucketInterval(window)
	table, _ := seriesSource(bucket)

	if bucket != time.Hour {
		t.Fatalf("bucketInterval(7d) = %s, want 1h", bucket)
	}
	if table != "ts_telemetry_hourly" {
		t.Fatalf("7d window routed to %s, want ts_telemetry_hourly to preserve hourly resolution", table)
	}
	if n := window / bucket; n > maxSeriesBuckets {
		t.Fatalf("7d window at 1h buckets = %d points, exceeds chart capacity", n)
	}
}
