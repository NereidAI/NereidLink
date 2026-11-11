// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package credcache provides an optional L1/L2 cache in front of device
// credential authentication (ADR-0014).
//
// The cache stores the verification material of a credential keyed by its
// globally unique credentials id; possession of the presented secret is
// proven locally through internal/credverify, so a cache hit performs no
// gRPC or PostgreSQL I/O. Eviction is therefore one delete per credential.
// The whole feature is constructed only when enabled in the platform
// configuration: with the default config ingresses authenticate exactly
// as before.
package credcache

import (
	"time"

	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"

	"github.com/nereidai/nereidlink/internal/credverify"
)

// Entry is one cached credential state. It is shared by the L1 and L2
// tiers and is JSON-serializable for NATS KV.
type Entry struct {
	// Negative marks a failed or unknown credential.
	Negative bool `json:"negative"`

	// Identity is the resolved device identity of a positive entry.
	Identity sdktelemetry.DeviceIdentity `json:"identity"`

	// Record is the stored verification material of a positive entry.
	Record credverify.Record `json:"record"`

	// Epoch is the tenant credential epoch stamped at resolution. It is
	// retained for diagnosis; eviction is event driven.
	Epoch int64 `json:"epoch"`

	// ExpiresAt bounds the credential validity; nil means no expiry. The
	// effective cache TTL is capped at it.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// CachedUntil bounds reuse of this cache entry independently of the
	// credential expiry. L2 has no per-key TTL in the client API, so the
	// authenticator enforces this field on every read.
	CachedUntil time.Time `json:"cached_until"`
}
