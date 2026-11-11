// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// hash_test.go verifies the cross-party invariants of the order
// content hash: stability, key-order independence and sensitivity to
// any change in scope, action or payload.

package opscenter

import (
	"encoding/json"
	"testing"
)

// TestCanonicalPayloadSortsKeys ensures differing key order and
// whitespace produce the same canonical document.
func TestCanonicalPayloadSortsKeys(t *testing.T) {
	a, err := canonicalPayload(json.RawMessage(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalPayload(json.RawMessage(`{ "a": 2, "b": 1 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical forms differ: %s vs %s", a, b)
	}
	if want := `{"a":2,"b":1}`; string(a) != want {
		t.Fatalf("got %s want %s", a, want)
	}
}

// TestCanonicalPayloadDefaults verifies nil/empty become {}.
func TestCanonicalPayloadDefaults(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`null`)} {
		got, err := canonicalPayload(raw)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{}` {
			t.Fatalf("got %s", got)
		}
	}
}

// TestCanonicalPayloadRejectsNonObjects prevents non-object orders.
func TestCanonicalPayloadRejectsNonObjects(t *testing.T) {
	for _, raw := range []string{`[1,2]`, `"x"`, `42`} {
		if _, err := canonicalPayload(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
	}
}

// TestContentHashStability checks the hash is reproducible.
func TestContentHashStability(t *testing.T) {
	h1, _, err := contentHash("tenant-1", ActionTelemetryPurge,
		json.RawMessage(`{"reason":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := contentHash("tenant-1", ActionTelemetryPurge,
		json.RawMessage(`{ "reason": "x" }`))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("hash not stable across equivalent payloads")
	}
}

// TestContentHashSensitivity ensures every bound field matters.
func TestContentHashSensitivity(t *testing.T) {
	base, _, _ := contentHash("tenant-1", ActionTelemetryPurge,
		json.RawMessage(`{"reason":"x"}`))
	cases := []struct {
		name   string
		tenant string
		action string
		input  string
	}{
		{"payload", "tenant-1", ActionTelemetryPurge, `{"reason":"y"}`},
		{"tenant", "tenant-2", ActionTelemetryPurge, `{"reason":"x"}`},
		{"action", "tenant-1", ActionForceDisconnect, `{"reason":"x"}`},
		{"global-scope", "", ActionTelemetryPurge, `{"reason":"x"}`},
	}
	for _, tc := range cases {
		got, _, err := contentHash(tc.tenant, tc.action,
			json.RawMessage(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatalf("hash unchanged when %s changed", tc.name)
		}
	}
}
