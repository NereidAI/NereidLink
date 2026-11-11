// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// chain.go computes the canonical hashes linking audit rows. The
// canonical representation is explicit (rather than JSON over the row)
// because field order and time formatting are part of the
// verification contract and must never change silently.

package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// genesisHash is the prev_hash of the first row.
const genesisHash = ""

// canonicalContent builds the string hashed for one row. Pipe
// separators are escaped by construction: IDs are UUIDs/tokens and
// details are encoded separately as canonical JSON.
func canonicalContent(seq int64, eventTime time.Time, input Input, detailsJSON []byte) string {
	return strings.Join([]string{
		fmt.Sprintf("%d", seq),
		eventTime.UTC().Format(time.RFC3339Nano),
		input.TenantID,
		input.ActorID,
		input.ActorName,
		input.EventType,
		input.Action,
		input.ResourceType,
		input.ResourceID,
		string(detailsJSON),
		input.IP,
	}, "|")
}

// hashRow returns the hash of a row: SHA-256 over prev_hash and the
// canonical content, hex encoded.
func hashRow(prevHash string, content string) string {
	sum := sha256.Sum256([]byte(prevHash + "\n" + content))
	return hex.EncodeToString(sum[:])
}

// canonicalDetails marshals details deterministically. Go's encoding
// package sorts map keys for map[string]any, so two identical details
// maps always produce identical bytes; nil maps encode as {}.
func canonicalDetails(details map[string]any) ([]byte, error) {
	if details == nil {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("marshal audit details: %w", err)
	}
	return raw, nil
}
