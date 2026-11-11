// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import (
	"testing"

	"github.com/nereidai/nereidlink/internal/ota"
)

func TestNormalizeOTAState(t *testing.T) {
	cases := map[string]string{
		"DOWNLOADING": ota.ItemStateDownloading,
		"DOWNLOADED":  ota.ItemStateDownloaded,
		"UPDATING":    ota.ItemStateUpdating,
		"FAILED":      ota.ItemStateFailed,
		"failed":      ota.ItemStateFailed,
		"SUCCESS":     ota.ItemStateSucceeded,
		"succeeded":   ota.ItemStateSucceeded,
		"BOGUS":       "",
		"":            "",
	}
	for in, want := range cases {
		if got := normalizeOTAState(in); got != want {
			t.Errorf("normalizeOTAState(%q)=%q want %q", in, got, want)
		}
	}
}
