// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package stepup issues and verifies the short-lived, one-time step-up
// challenges used to re-authenticate a human operator immediately before a
// dangerous action (ADR-0035; reused by later protected flows such as
// config-bundle export/import).
//
// Lifecycle:
//
//	pending --TOTP verified--> verified --consumed by the protected
//	endpoint (exactly once)--> consumed.
//
// Design constraints:
//   - The layer carries no business vocabulary. An "action" is an opaque key
//     the calling feature registers and maps to an RBAC point; only its
//     non-sensitive details are stored and shown.
//   - Machine principals never reach it: HTTP wiring refuses them before
//     Start.
//   - TOTP verification itself, including the existing failure-lockout
//     policy, stays in identity behind the TOTPVerifier interface.
//   - Challenges persist so a restart does not invalidate or duplicate them,
//     and so replay of an already-consumed challenge is detectable and
//     auditable.
package stepup

import (
	"errors"
	"time"
)

// Challenge status values.
const (
	// StatusPending is issued before the TOTP code is presented.
	StatusPending = "pending"
	// StatusVerified means the fresh TOTP code passed.
	StatusVerified = "verified"
	// StatusConsumed means the challenge was presented to a protected
	// endpoint; it can never be used again.
	StatusConsumed = "consumed"
)

// TTL is the challenge lifetime covering start, TOTP verification and use.
const TTL = 5 * time.Minute

// Sentinel errors returned by the service and stores.
var (
	// ErrNotFound means no challenge exists for the caller and id.
	ErrNotFound = errors.New("step-up challenge not found")
	// ErrExpired means the challenge passed its 5-minute lifetime.
	ErrExpired = errors.New("step-up challenge expired")
	// ErrNotPending means a code was presented for a non-pending row.
	ErrNotPending = errors.New("step-up challenge is not awaiting verification")
	// ErrNotVerified means consume saw a row that never completed TOTP.
	ErrNotVerified = errors.New("step-up challenge was not verified")
	// ErrReplay means an already-consumed challenge was presented again.
	ErrReplay = errors.New("step-up challenge was already consumed")
	// ErrActionMismatch means the challenge was consumed for an action
	// other than the one it was started for.
	ErrActionMismatch = errors.New("step-up challenge action mismatch")
)

// Challenge is one persisted challenge row.
type Challenge struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"tenant_id"`
	UserID        string         `json:"user_id"`
	Action        string         `json:"action"`
	Details       map[string]any `json:"details"`
	DetailsSHA256 string         `json:"details_sha256"`
	Status        string         `json:"status"`
	ExpiresAt     time.Time      `json:"expires_at"`
	VerifiedAt    *time.Time     `json:"verified_at,omitempty"`
	ConsumedAt    *time.Time     `json:"consumed_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`

	// SignedPayload is populated on responses (never stored): the signed
	// QR content describing action, details hash and expiry.
	SignedPayload string `json:"signed_payload,omitempty"`
}

// StartInput starts one challenge for the calling human.
type StartInput struct {
	TenantID string
	UserID   string
	Action   string
	Details  map[string]any
}
