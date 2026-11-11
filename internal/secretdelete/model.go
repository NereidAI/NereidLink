// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package secretdelete implements the vault secret deletion guard from
// ADR-0034 (ticket 07). A secret still delivered to one or more gateways
// cannot be hard-deleted by a normal delete: callers first see the list of
// referencing gateways. A forced delete withdraws every binding
// declaratively (REMOVING; the edge confirms REMOVED on its next sync) and
// defers the vault hard-delete until no binding remains.
//
// Offline gateways cannot confirm removal immediately, so the pending row
// in tenant_secret_pending_delete is the durable "deleting" marker. It is
// finalized either by the post-REMOVED hook from the binding service or by
// the periodic sweep, both converging on the same finalizeIfDone check.
package secretdelete

import "time"

// PendingDelete is one persisted forced-deletion marker (migration 0073).
type PendingDelete struct {
	// TenantID owns the vault entry.
	TenantID string `json:"tenant_id"`
	// SecretName is the vault key being deleted.
	SecretName string `json:"secret_name"`
	// InitiatedBy is the human operator's account id.
	InitiatedBy string `json:"initiated_by,omitempty"`
	// InitiatedAt is when the forced deletion started (RFC3339 UTC).
	InitiatedAt string `json:"initiated_at"`
}

// Actor identifies the human operator requesting the forced deletion.
type Actor struct {
	// UserID is the account id.
	UserID string
	// Email is the login name for readable audit.
	Email string
}

// DefaultSweepInterval is how often pending rows are re-checked. The
// post-REMOVED hook normally finalizes sooner; the sweep is the durable
// path after a missed notification.
const DefaultSweepInterval = time.Hour
