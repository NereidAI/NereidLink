// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"strings"

	"github.com/google/uuid"
)

// Persistence worker tuning. These are internal writer mechanics rather
// than user-facing operational tunables.
const (
	// persistFlushInterval bounds the delay of queued history writes.
	persistFlushInterval = 200_000_000

	// persistBatchSize triggers an immediate flush.
	persistBatchSize = 256
)

// uuidString returns a fresh random UUID string used as a fencing session
// identifier.
func uuidString() string {
	return uuid.NewString()
}

// splitPresenceKey reverses presenceKey, returning the tenant and device
// identifiers. UUIDs never contain dots, so the first dot is the boundary.
func splitPresenceKey(key string) (tenantID, deviceID string, ok bool) {
	tenantID, deviceID, found := strings.Cut(key, ".")
	if !found || tenantID == "" || deviceID == "" {
		return "", "", false
	}
	return tenantID, deviceID, true
}
