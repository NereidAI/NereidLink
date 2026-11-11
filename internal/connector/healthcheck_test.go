// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestNormalizeHealthCheck verifies defaulting, the disabled states and the
// bound validation of the probe policy.
func TestNormalizeHealthCheck(t *testing.T) {
	t.Run("nil and empty are disabled", func(t *testing.T) {
		for _, raw := range []json.RawMessage{nil, json.RawMessage("null"),
			json.RawMessage(`{}`), json.RawMessage(``)} {
			got, err := normalizeHealthCheck(raw)
			if err != nil || got != nil {
				t.Fatalf("raw=%s got=%v err=%v, want disabled", raw, got, err)
			}
		}
	})

	t.Run("defaults applied", func(t *testing.T) {
		got, err := normalizeHealthCheck(json.RawMessage(`{"resource":"camera_status"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Resource != "camera_status" ||
			got.IntervalMS != DefaultHealthIntervalMS ||
			got.TimeoutMS != DefaultHealthTimeoutMS ||
			got.FailThreshold != DefaultHealthFailThreshold {
			t.Fatalf("unexpected: %+v", got)
		}
		if !got.Enabled() {
			t.Fatal("Enabled() = false, want true")
		}
	})

	t.Run("stray fields without resource rejected", func(t *testing.T) {
		_, err := normalizeHealthCheck(json.RawMessage(`{"interval_ms":30000}`))
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("err=%v, want ErrInvalid", err)
		}
	})

	for name, raw := range map[string]string{
		"bad json":            `{`,
		"interval too high":   `{"resource":"r","interval_ms":999999999}`,
		"timeout too high":    `{"resource":"r","timeout_ms":999999}`,
		"threshold out range": `{"resource":"r","fail_threshold":101}`,
		"timeout >= interval": `{"resource":"r","interval_ms":5000,"timeout_ms":6000}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeHealthCheck(json.RawMessage(raw))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v, want ErrInvalid", err)
			}
		})
	}
}

// TestHealthCheckColumn ensures the disabled policy serializes to an empty
// object for the NOT NULL JSONB column.
func TestHealthCheckColumn(t *testing.T) {
	col, err := healthCheckColumn(nil)
	if err != nil || string(col) != "{}" {
		t.Fatalf("disabled column = %s, %v", col, err)
	}
	hc := &HealthCheck{Resource: "r", IntervalMS: 10000, TimeoutMS: 2000, FailThreshold: 2}
	col, err = healthCheckColumn(hc)
	if err != nil {
		t.Fatal(err)
	}
	var back HealthCheck
	if err := json.Unmarshal(col, &back); err != nil || back != *hc {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}
