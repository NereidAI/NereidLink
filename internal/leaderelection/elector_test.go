// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package leaderelection

import "testing"

func TestSanitizeKey(t *testing.T) {
	cases := map[string]string{
		"silence":           "silence",
		"edge_secret:force": "edge-secret-force",
		"ops/sweep":         "ops-sweep",
		"Rule_Processor":    "rule-processor",
		"spaces here":       "spaces-here",
	}
	for in, want := range cases {
		if got := sanitizeKey(in); got != want {
			t.Errorf("sanitizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
