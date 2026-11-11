// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// errors.go defines shadow service sentinel errors.

package shadow

import "errors"

var (
	// ErrDeviceNotFound means the target device does not exist.
	ErrDeviceNotFound = errors.New("shadow target device not found")

	// ErrETagMismatch means the caller expected a different desired version.
	ErrETagMismatch = errors.New("shadow etag mismatch")

	// ErrInvalidPatch means the update document is malformed.
	ErrInvalidPatch = errors.New("invalid shadow patch")
)
