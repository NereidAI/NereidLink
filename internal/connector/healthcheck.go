// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// healthcheck.go defines the instance-level liveness probe policy attached to
// a desired connector spec. The NL kernel validates and stores the policy but
// never executes it: the owning edge gateway periodically READS the named
// south resource, DISCARDS THE VALUE and autonomously asserts the bound
// sub-device connect/disconnect based on consecutive probe outcomes.
//
// The field is intentionally free-form JSONB in storage; this typed view is
// the single place that fixes the wire contract shared with every edge
// implementation. It must stay protocol-agnostic: "resource" is the same
// neutral resource name space the connector mappings use, never a
// vendor-specific URL or command path.

package connector

import (
	"encoding/json"
	"fmt"
)

const (
	// DefaultHealthIntervalMS is used when the operator enables a probe
	// without setting a cadence.
	DefaultHealthIntervalMS = 30_000
	// DefaultHealthTimeoutMS bounds one probe read.
	DefaultHealthTimeoutMS = 5000
	// DefaultHealthFailThreshold is the consecutive-failure count that
	// flips the bound device to disconnected.
	DefaultHealthFailThreshold = 3

	maxHealthResourceLen = 256
	maxHealthIntervalMS  = 24 * 3600 * 1000
	maxHealthTimeoutMS   = 60_000
	maxHealthThreshold   = 100
)

// HealthCheck is the desired liveness probe policy for one connector
// instance. A zero value (Enabled()==false) disables probing.
type HealthCheck struct {
	// Resource names the south resource to read (a read command / point
	// resource name in the connector's neutral mapping space).
	Resource string `json:"resource,omitempty"`

	// IntervalMS is the probe cadence in milliseconds.
	IntervalMS int `json:"interval_ms,omitempty"`

	// TimeoutMS bounds one probe read.
	TimeoutMS int `json:"timeout_ms,omitempty"`

	// FailThreshold is how many consecutive failed probes flip the bound
	// device to disconnected; the first success reconnects it.
	FailThreshold int `json:"fail_threshold,omitempty"`
}

// Enabled reports whether probing is requested.
func (h HealthCheck) Enabled() bool {
	return h.Resource != ""
}

// normalize fills defaults and validates bounds. It returns the canonical
// document that should be persisted and forwarded. A nil result with a nil
// error means "probe disabled" (empty object).
func normalizeHealthCheck(raw json.RawMessage) (*HealthCheck, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var h HealthCheck
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("%w: health_check: %v", ErrInvalid, err)
	}
	if h.Resource == "" {
		// Only empty documents are allowed past this point; a document
		// carrying stray non-resource fields is rejected rather than
		// silently dropped, to catch contract drift.
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("%w: health_check: %v", ErrInvalid, err)
		}
		if len(probe) != 0 {
			return nil, fmt.Errorf("%w: health_check: resource is required", ErrInvalid)
		}
		return nil, nil
	}
	if len(h.Resource) > maxHealthResourceLen {
		return nil, fmt.Errorf("%w: health_check.resource too long", ErrInvalid)
	}
	if h.IntervalMS == 0 {
		h.IntervalMS = DefaultHealthIntervalMS
	}
	if h.TimeoutMS == 0 {
		h.TimeoutMS = DefaultHealthTimeoutMS
	}
	if h.FailThreshold == 0 {
		h.FailThreshold = DefaultHealthFailThreshold
	}
	if h.IntervalMS < 1 || h.IntervalMS > maxHealthIntervalMS {
		return nil, fmt.Errorf("%w: health_check.interval_ms out of range", ErrInvalid)
	}
	if h.TimeoutMS < 1 || h.TimeoutMS > maxHealthTimeoutMS {
		return nil, fmt.Errorf("%w: health_check.timeout_ms out of range", ErrInvalid)
	}
	if h.FailThreshold < 1 || h.FailThreshold > maxHealthThreshold {
		return nil, fmt.Errorf("%w: health_check.fail_threshold out of range", ErrInvalid)
	}
	// Both bounds are in the same unit (ms), so this is a direct compare.
	if h.TimeoutMS >= h.IntervalMS {
		return nil, fmt.Errorf("%w: health_check.timeout_ms must be shorter than interval_ms", ErrInvalid)
	}
	return &h, nil
}

// healthCheckColumn converts the validated policy into its JSONB column
// value: an empty JSON object when disabled.
func healthCheckColumn(h *HealthCheck) ([]byte, error) {
	if h == nil || !h.Enabled() {
		return []byte("{}"), nil
	}
	return json.Marshal(h)
}
