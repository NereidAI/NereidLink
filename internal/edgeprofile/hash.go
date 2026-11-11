// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalHash returns the lowercase-hex sha256 over the compact,
// deterministically-keyed JSON of one edge profile. It must agree with the
// device SDK helper device.CanonicalProfileHash: both types carry identical
// JSON tags and encoding/json emits struct fields and map keys in a fixed
// order, so the edge and the platform compute the same digest for the same
// profile even though they live in different Go modules.
func CanonicalHash(profile Profile) (string, error) {
	raw, err := json.Marshal(profile)
	if err != nil {
		return "", fmt.Errorf("encode canonical edge profile: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// contentHashOf returns a lowercase-hex sha256 over arbitrary raw document
// bytes. It backs paste-import drift detection; unlike CanonicalHash it makes
// no cross-module agreement promise because the document is authored locally.
func contentHashOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
