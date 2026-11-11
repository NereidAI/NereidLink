// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// time_window.go parses and bounds the start/end query parameters shared by
// the telemetry history, keys and series endpoints (ADR-0017 query guards).
// A malformed timestamp, a reversed window or a window past the endpoint's
// range cap is rejected at the API boundary instead of silently degrading
// into a default window or an unbounded hypertable scan.

package gatewayapi

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// ErrInvalidTimeWindow marks a malformed or out-of-range start/end pair. The
// REST layer maps it to a 400.
var ErrInvalidTimeWindow = errors.New("invalid time window")

func badWindow(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTimeWindow, fmt.Sprintf(format, args...))
}

// parseTimeWindow resolves the [start,end] window from the query string. When
// a bound is omitted it defaults to now-def (start) or now (end), so a bare
// request returns the most recent default window. It validates that every
// supplied
// timestamp is RFC3339, that start precedes end, and that the span never
// exceeds max, which keeps a query from scanning an arbitrarily large slice of
// the raw hypertable (long ranges must use the aggregated series endpoint).
func parseTimeWindow(query url.Values, def, max time.Duration) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	start := now.Add(-def)
	end := now

	if raw := query.Get("start"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, badWindow("start must be an RFC3339 timestamp")
		}
		start = parsed.UTC()
	}
	if raw := query.Get("end"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, badWindow("end must be an RFC3339 timestamp")
		}
		end = parsed.UTC()
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, badWindow("start must be earlier than end")
	}
	if span := end.Sub(start); span > max {
		return time.Time{}, time.Time{},
			badWindow("requested time range is too wide for this endpoint; use the aggregated series endpoint for long ranges")
	}
	return start, end, nil
}
