// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package buscodec implements application-layer compression for messages
// crossing the internal message bus (ADR-0003). It offers four selectable
// levels and self-describing framed payloads so producers and consumers
// stay symmetric.
//
// Levels:
//
//   - off:        bytes pass through unchanged.
//   - fast:       S2 block encoding (LZ4-class, lowest CPU cost).
//   - balanced:   zstd default encoder (bandwidth/CPU compromise).
//   - max:        zstd best-compression encoder.
//
// Frames carry a three-byte magic plus a codec id, which lets a consumer
// tell a compressed message from an uncompressed one. That distinction is
// what makes level changes hot-effective: messages already stored in
// JetStream keep their original form and are still decoded correctly, and
// no history is recompressed. Messages smaller than MinBytes and payloads
// whose content type is already compressed are left untouched.
package buscodec

import (
	"errors"
	"strings"
)

// Level names accepted by ParseLevel and SetLevel.
const (
	// LevelOff disables compression.
	LevelOff = "off"

	// LevelFast selects the S2 codec.
	LevelFast = "fast"

	// LevelBalanced selects the zstd default level.
	LevelBalanced = "balanced"

	// LevelMax selects the zstd best-compression level.
	LevelMax = "max"
)

// ErrInvalidLevel means a configuration value names no known level.
var ErrInvalidLevel = errors.New("invalid bus compression level")

// ParseLevel normalizes a level string, rejecting unknown values so a
// typo in the config fails loudly instead of silently disabling
// compression.
func ParseLevel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case LevelOff:
		return LevelOff, nil
	case LevelFast:
		return LevelFast, nil
	case LevelBalanced:
		return LevelBalanced, nil
	case LevelMax:
		return LevelMax, nil
	default:
		return "", ErrInvalidLevel
	}
}
