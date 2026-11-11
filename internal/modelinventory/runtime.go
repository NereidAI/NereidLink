// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package modelinventory

import (
	"encoding/json"
	"strconv"
	"time"
)

// parseRuntimeSnapshot decodes the free-form device-shadow reported
// "inference" block (ADR-0039 §3.5). The shadow stores reported state
// verbatim with no kernel-defined schema, so this read is intentionally
// tolerant: an absent or unparseable block returns Present=false and the
// watchdog skips that gateway rather than raising a false alarm.
//
// Shape:
//
//	inference:{state,endpoint?,protocol,models_ready,models_total,
//	           active_bindings,checked_at,probe_interval_s,latency_ms?}
func parseRuntimeSnapshot(block map[string]any) RuntimeSnapshot {
	if len(block) == 0 {
		return RuntimeSnapshot{}
	}
	s := RuntimeSnapshot{Present: true}
	s.State = asString(block["state"])
	s.Endpoint = asString(block["endpoint"])
	s.Protocol = asString(block["protocol"])
	s.ModelsReady = int(asFloat(block["models_ready"]))
	s.ModelsTotal = int(asFloat(block["models_total"]))
	s.ActiveBindings = int(asFloat(block["active_bindings"]))
	s.LatencyMS = asFloat(block["latency_ms"])
	s.ProbeIntervalS = asFloat(block["probe_interval_s"])
	s.CheckedAt = asTime(block["checked_at"])
	return s
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

// asTime accepts an RFC3339 string (the usual shadow encoding) or a numeric
// unix-seconds value. Anything else returns the zero time, which the caller
// treats as "freshness unknown".
func asTime(v any) time.Time {
	switch t := v.(type) {
	case string:
		if t == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return parsed.UTC()
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed.UTC()
		}
	case float64:
		return time.Unix(int64(t), int64((t-float64(int64(t)))*1e9)).UTC()
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return time.Unix(int64(f), int64((f-float64(int64(f)))*1e9)).UTC()
		}
	}
	return time.Time{}
}
