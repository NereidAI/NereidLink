// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import (
	"testing"
)

// TestDecodeProbeResponses covers the legacy per-device uplink and the
// optional batched form, including dropping malformed entries and inheriting
// the frame-level probe id per entry.
func TestDecodeProbeResponses(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    int
		firstID string
	}{
		{"single frame", `{"probe_id":"t1","device":"d1","result":{"ok":true}}`, 1, "t1"},
		{"bulk frame", `{"probe_id":"t2","responses":[
			{"device":"d1","result":{"ok":true}},
			{"device":"d2","error":"no response"}]}`, 2, "t2"},
		{"bulk entry overrides probe id", `{"probe_id":"t3","responses":[
			{"probe_id":"t4","device":"d1"}]}`, 1, "t4"},
		{"bulk drops entry without device", `{"probe_id":"t5","responses":[
			{"result":{"ok":true}},{"device":"d1"}]}`, 1, "t5"},
		{"malformed", `{"probe_id":`, 0, ""},
		{"single missing device", `{"probe_id":"t6","result":{}}`, 0, ""},
		{"empty bulk falls back to single", `{"probe_id":"t7","device":"d9"}`, 1, "t7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeProbeResponses([]byte(tc.payload))
			if len(got) != tc.want {
				t.Fatalf("want %d frames, got %d (%+v)", tc.want, len(got), got)
			}
			if tc.want > 0 && got[0].ProbeID != tc.firstID {
				t.Fatalf("first probe id: want %s, got %s", tc.firstID, got[0].ProbeID)
			}
		})
	}
}
