// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestParseTimeWindow(t *testing.T) {
	const def = 24 * time.Hour
	const max = 7 * 24 * time.Hour
	start := "2026-10-10T00:00:00Z"
	end := "2026-10-11T00:00:00Z"

	t.Run("defaults when absent", func(t *testing.T) {
		s, e, err := parseTimeWindow(url.Values{}, def, max)
		if err != nil {
			t.Fatal(err)
		}
		if span := e.Sub(s); span > def+time.Second || span < def-time.Second {
			t.Fatalf("default span %v, want %v", span, def)
		}
	})

	t.Run("accepts explicit window", func(t *testing.T) {
		q := url.Values{}
		q.Set("start", start)
		q.Set("end", end)
		s, e, err := parseTimeWindow(q, def, max)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC); !s.Equal(want) {
			t.Fatalf("start=%v want %v", s, want)
		}
		if e.Sub(s) != 24*time.Hour {
			t.Fatalf("span=%v want 24h", e.Sub(s))
		}
	})

	t.Run("accepts offset timestamp normalized to UTC", func(t *testing.T) {
		q := url.Values{}
		q.Set("start", "2026-10-10T08:00:00+08:00")
		q.Set("end", end)
		s, _, err := parseTimeWindow(q, def, max)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC); !s.Equal(want) {
			t.Fatalf("offset start=%v want %v", s, want)
		}
	})

	t.Run("exactly the cap is allowed", func(t *testing.T) {
		q := url.Values{}
		q.Set("start", "2026-10-04T00:00:00Z")
		q.Set("end", "2026-10-11T00:00:00Z")
		if _, _, err := parseTimeWindow(q, def, max); err != nil {
			t.Fatalf("span == cap must be allowed, got %v", err)
		}
	})

	bad := []struct {
		name string
		q    url.Values
	}{
		{"bad start", url.Values{"start": {"nope"}, "end": {end}}},
		{"bad end", url.Values{"start": {start}, "end": {"nope"}}},
		{"start equals end", url.Values{"start": {end}, "end": {end}}},
		{"start after end", url.Values{"start": {end}, "end": {start}}},
		{"window wider than cap", url.Values{
			"start": {"2026-10-03T23:59:59Z"}, "end": {end}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseTimeWindow(tc.q, def, max)
			if !errors.Is(err, ErrInvalidTimeWindow) {
				t.Fatalf("want ErrInvalidTimeWindow, got %v", err)
			}
		})
	}
}
