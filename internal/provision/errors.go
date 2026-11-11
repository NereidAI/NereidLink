// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// errors.go defines the sentinel errors returned by the provisioning
// service; handlers map them to standard REST status codes.

package provision

import "errors"

var (
	// ErrNotFound indicates the provisioning task does not exist.
	ErrNotFound = errors.New("provisioning task not found")

	// ErrInvalidArgument indicates a malformed provisioning request.
	ErrInvalidArgument = errors.New("invalid provisioning argument")
)
