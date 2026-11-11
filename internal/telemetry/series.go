// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// series.go implements window-aware downsampled telemetry lookup. A raw
// history page cannot cover a long window: it returns only the newest N
// rows, which span minutes regardless of the requested range. Here rows
// are grouped into a bounded number of time_bucket buckets and the last
// value of every bucket is returned, so one query covers any window.

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// hourlyRollup names the hourly continuous aggregate created by migration
// 0024. Coverage is probed against the hourly rollup because it is the
// finer of the two: if hourly has caught up, daily is derived from it and
// cannot be behind.
const hourlyRollup = "ts_telemetry_hourly"

// SeriesPoint is one downsampled bucket value.
type SeriesPoint struct {
	// Time is the bucket start as RFC3339 UTC.
	Time string `json:"time"`

	// ValueType selects the populated value carrier.
	ValueType string `json:"value_type"`

	// VDouble carries the last numeric value of the bucket.
	VDouble *float64 `json:"v_double,omitempty"`

	// VBool carries the last boolean value of the bucket.
	VBool *bool `json:"v_bool,omitempty"`

	// VString carries the last string value of the bucket.
	VString string `json:"v_string,omitempty"`
}

// bucketInterval returns a "nice" interval such that the window spans no
// more than maxBuckets buckets. Nice boundaries keep bucket edges aligned
// to familiar clock units.
//
// The candidate list must reach past 24h. It used to stop there and fall back
// to 24h, which silently violated its own contract for any window longer than
// maxSeriesBuckets days: a 365-day window produced 365 buckets, three times
// the chart's capacity, and the response grew with the range. 48h and 7d are
// included so multi-quarter windows still fit inside maxSeriesBuckets.
func bucketInterval(window time.Duration) time.Duration {
	for _, candidate := range []time.Duration{
		5 * time.Second,
		15 * time.Second,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		time.Hour,
		3 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
		48 * time.Hour,
		7 * 24 * time.Hour,
	} {
		if window/candidate <= maxSeriesBuckets {
			return candidate
		}
	}
	// Unreachable for any window a caller can express, but returning the
	// largest candidate rather than 24h keeps the invariant true by
	// construction instead of by luck.
	return 7 * 24 * time.Hour
}

// maxSeriesBuckets bounds the number of returned buckets; it matches the
// chart's capacity and keeps one response small at any window.
const maxSeriesBuckets = 240

// seriesSource picks the relation a series query should read.
//
// The rollups added by migration 0024 are hourly and daily, so a rollup can
// only answer a query whose bucket is at least as coarse as its own. Routing
// is therefore keyed on the requested bucket width rather than on the raw
// window span:
//
//   - ADR-0004 phrases the rule as "span >= 1d goes hourly, >= 7d goes daily".
//     Keying on span instead would silently coarsen the chart, because
//     bucketInterval already picks the finest interval that fits within
//     maxSeriesBuckets -- a 7-day window yields hourly buckets today, and
//     serving those from the daily rollup would collapse 168 points into 7.
//     Resolution is part of the chart's contract with the operator, so the
//     bucket width decides which relation answers, not the span.
//
// Both rollups are created with materialized_only = false, which unions live
// hypertable rows past the refresh watermark. That union is bounded by the
// refresh window and is NOT a backfill: a rollup that has never been
// refreshed has no history, so routing to it would return an empty chart for
// every historical window. readsTelemetryRollups therefore gates routing on
// proven coverage, and the hypertable remains the answer whenever coverage is
// unproven -- including when migration 0024 has not been applied at all.
//
// The returned table names are compile-time constants, never user input, so
// interpolating them into the statement cannot carry an injection.
func seriesSource(bucket time.Duration) (table, timeColumn string) {
	switch {
	case bucket >= 24*time.Hour:
		return "ts_telemetry_daily", "bucket"
	case bucket >= time.Hour:
		return "ts_telemetry_hourly", "bucket"
	default:
		return "ts_telemetry", "time"
	}
}

// seriesRelation returns the relation and time column to read for a bucket
// width, falling back to the hypertable unless the rollups are proven to
// cover the stored data.
func (s *Server) seriesRelation(bucket time.Duration) (table, timeColumn string) {
	table, timeColumn = seriesSource(bucket)
	if table == "ts_telemetry" {
		return table, timeColumn
	}
	if !s.readsTelemetryRollups() {
		return "ts_telemetry", "time"
	}
	return table, timeColumn
}

// rollupCheck is the coverage probe issued once per process.
//
// Both ends are compared, not just the newest, because a refresh interrupted
// partway leaves an earlier gap that a tail-only check would miss.
//
// The comparison is made at bucket granularity, and that detail is load
// bearing: a bucket is truncated to its start, so the newest raw row at
// 16:08 lives in the bucket starting 16:00. Comparing max(bucket) directly
// against max(time) asks whether 16:00 >= 16:08, which is never true, and the
// guard would silently disable routing forever. Bucketing the raw maximum
// first compares like with like.
const rollupCheck = `
	SELECT COALESCE((SELECT min(bucket) FROM ` + hourlyRollup + `), '-infinity'::timestamptz)
	    <= time_bucket('1 hour', COALESCE((SELECT min(time) FROM ts_telemetry), '-infinity'::timestamptz))
	   AND COALESCE((SELECT max(bucket) FROM ` + hourlyRollup + `), '-infinity'::timestamptz)
	    >= time_bucket('1 hour', COALESCE((SELECT max(time) FROM ts_telemetry), '-infinity'::timestamptz))`

// readsTelemetryRollups reports whether the continuous aggregates have been
// backfilled over the raw data they summarize.
//
// The failure this guards against is silent and therefore expensive: a
// rollup that exists but was never refreshed answers every historical query
// with zero rows, and the device chart renders empty. That looks identical to
// a device that stopped reporting, so an operator would chase a phantom
// ingest bug instead of a missing migration step. Proving coverage once at
// startup costs two indexed max() probes and removes the ambiguity.
//
// The probe deliberately tolerates failure. If migration 0024 has not been
// applied, the relation is missing and the query errors; that is treated as
// "not covered" so a pre-0024 database keeps reading the hypertable exactly
// as it did before.
func (s *Server) readsTelemetryRollups() bool {
	s.rollupOnce.Do(func() {
		var covered bool
		if err := s.pool.QueryRow(context.Background(), rollupCheck).Scan(&covered); err != nil {
			slog.Warn("telemetry rollups unavailable, reading the hypertable directly",
				"reason", "coverage check failed", "err", err)
			s.rollupReady = false
			return
		}
		s.rollupReady = covered
		if !covered {
			slog.Warn("telemetry rollups are not backfilled, reading the hypertable directly",
				"hint", "run `nereidlink migrate up` so the rollup backfill completes")
		}
	})
	return s.rollupReady
}

// Series returns downsampled values of one measurement key within a
// window, ascending by bucket.
func (s *Server) Series(ctx context.Context, tenantID, deviceID, key string, start, end time.Time) ([]SeriesPoint, error) {
	bucket := bucketInterval(end.Sub(start))
	table, timeColumn := s.seriesRelation(bucket)

	// The aggregate set is identical across all three relations, so one query
	// shape serves the hypertable and both rollups. Re-bucketing a rollup to a
	// coarser interval is a no-op regroup; it is only ever a regroup to a
	// coarser interval, never finer, because seriesSource guarantees the
	// rollup's own width is <= the requested bucket.
	// Aggregated series is just as device-derived as raw history; gate it with
	// the caller's device visibility so rollups cannot be read for a device
	// outside their scope. Parameters $1-$6 are already bound, so the gate
	// starts at $7.
	seriesScope := rbac.ScopeFromContext(ctx)
	seriesGate, seriesGateArgs := seriesScope.DeviceSubqueryPredicate("device_id", 6)
	rows, err := s.pool.Query(ctx,
		`SELECT time_bucket($6, `+timeColumn+`) AS bucket,
		        last(v_double, `+timeColumn+`),
		        last(v_bool, `+timeColumn+`),
		        last(v_string, `+timeColumn+`)
		 FROM `+table+`
		 WHERE tenant_id = $1 AND device_id = $2
		   AND measurement_key = $3
		   AND `+timeColumn+` >= $4 AND `+timeColumn+` <= $5`+seriesGate+`
		 GROUP BY bucket
		 ORDER BY bucket`,
		append([]any{tenantID, deviceID, key, start, end, bucket}, seriesGateArgs...)...)
	if err != nil {
		return nil, fmt.Errorf("query telemetry series: %w", err)
	}
	defer rows.Close()

	points := []SeriesPoint{}
	for rows.Next() {
		var point SeriesPoint
		var bucketTime time.Time
		var vDouble *float64
		var vBool *bool
		var vString *string
		if err := rows.Scan(&bucketTime, &vDouble, &vBool, &vString); err != nil {
			return nil, fmt.Errorf("scan series bucket: %w", err)
		}
		point.Time = bucketTime.UTC().Format(time.RFC3339)
		switch {
		case vBool != nil:
			point.ValueType = "boolean"
			point.VBool = vBool
		case vString != nil && *vString != "":
			point.ValueType = "string"
			point.VString = *vString
		default:
			point.ValueType = "number"
			point.VDouble = vDouble
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}
