// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// hash.go computes the order content hash. Both ends of the review
// must derive the identical value, so the serialized form is fully
// canonical: a fixed version line, the tenant scope, the action and a
// compact JSON document whose object keys are sorted.

package opscenter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// hashVersion pins the canonical serialization format; any future
// change requires a new version line rather than silent divergence.
const hashVersion = "opsorder-v1"

// canonicalPayload round-trips raw through generic values so object
// keys are sorted and whitespace normalized. Numbers take Go's
// normalized JSON number form; every party applies the same transform,
// so hashes stay comparable.
func canonicalPayload(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}
	return canonical, nil
}

// contentHash renders the canonical string and returns its SHA-256 hex.
// An empty tenantID is encoded as "*" for platform-global orders.
func contentHash(tenantID, action string, payload json.RawMessage) (string, json.RawMessage, error) {
	canonical, err := canonicalPayload(payload)
	if err != nil {
		return "", nil, err
	}
	scope := tenantID
	if scope == "" {
		scope = "*"
	}
	sum := sha256.Sum256([]byte(
		hashVersion + "\n" + scope + "\n" + action + "\n" + string(canonical)))
	return hex.EncodeToString(sum[:]), canonical, nil
}
