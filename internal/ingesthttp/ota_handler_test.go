// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingesthttp

import "testing"

func TestParseSingleRange(t *testing.T) {
	const size = 100

	cases := []struct {
		name   string
		header string
		offset int64
		length int64
		ranged bool
		ok     bool
	}{
		{"absent", "", 0, 0, false, true},
		{"open ended", "bytes=10-", 10, 0, true, true},
		{"closed", "bytes=10-19", 10, 10, true, true},
		{"suffix", "bytes=-20", 80, 20, true, true},
		{"suffix clamped", "bytes=-200", 0, 100, true, true},
		{"end clamped to size", "bytes=90-500", 90, 10, true, true},
		{"start at size rejected", "bytes=100-", 0, 0, false, false},
		{"reversed", "bytes=50-10", 0, 0, false, false},
		{"multipart rejected", "bytes=0-10,20-30", 0, 0, false, false},
		{"bad unit", "items=0-10", 0, 0, false, false},
		{"garbage", "bytes=abc-def", 0, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off, length, ranged, ok := parseSingleRange(tc.header, size)
			if ok != tc.ok || ranged != tc.ranged || off != tc.offset || length != tc.length {
				t.Fatalf("got (%d,%d,ranged=%v,ok=%v) want (%d,%d,ranged=%v,ok=%v)",
					off, length, ranged, ok, tc.offset, tc.length, tc.ranged, tc.ok)
			}
		})
	}
}
