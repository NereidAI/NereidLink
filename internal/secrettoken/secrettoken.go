// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package secrettoken generates high-entropy, case-safe tokens shared by
// long-lived credential implementations. The alphabet is the unpadded
// lowercase RFC 4648 base32 alphabet, which is safe in shell history,
// configuration files and HTTP headers.
package secrettoken

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
)

// Encoding is the lowercase, unpadded base32 encoding used for credentials.
var Encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").
	WithPadding(base32.NoPadding)

// Random returns prefix followed by the unpadded lowercase base32 encoding
// of entropyBytes random bytes.
func Random(prefix string, entropyBytes int) (string, error) {
	if entropyBytes <= 0 {
		return "", fmt.Errorf("secrettoken: entropy bytes must be positive")
	}
	raw := make([]byte, entropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + Encoding.EncodeToString(raw), nil
}
