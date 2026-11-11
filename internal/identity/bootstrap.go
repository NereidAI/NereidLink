// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// bootstrap.go implements first-run administrator provisioning. A freshly
// migrated database contains one platform admin
// (admin@nereidlink.local) with an empty password_hash and
// must_change_password=TRUE. Before that account has a password nobody can
// log in; the unauthenticated bootstrap endpoint lets the first visitor set
// it exactly once. The atomic conditional UPDATE (hash must still be empty)
// makes two racing first visitors safe: exactly one wins.

package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nereidai/nereidlink/internal/audit"
)

// BootstrapAdminEmail is the deterministic first-run platform administrator
// provisioned by migration 0049, renamed by 0084.
const BootstrapAdminEmail = "admin@nereidai.local"

// ErrBootstrapComplete means the administrator password was already set, so
// the first-run endpoint can no longer be used.
var ErrBootstrapComplete = errors.New("bootstrap already complete")

// BootstrapStatus reports whether first-run setup is still required.
type BootstrapStatus struct {
	// Required is true while the bootstrap admin has no password.
	Required bool `json:"required"`
	// Email is the account the first password will be set on.
	Email string `json:"email,omitempty"`
}

// BootstrapStatus reports whether first-run setup is still pending. It is
// safe to expose unauthenticated (it reveals only the admin email, which is
// fixed) and drives the UI's setup screen.
func (s *Service) BootstrapStatus(ctx context.Context) (BootstrapStatus, error) {
	status := BootstrapStatus{}
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(password_hash, '')
		 FROM app_user
		 WHERE email = $1 AND tenant_id IS NULL
		 ORDER BY created_at
		 LIMIT 1`, BootstrapAdminEmail).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Defensive: migrations normally create the account, but a stripped
		// database must not report "already configured" and lock the UI.
		return BootstrapStatus{Required: true, Email: BootstrapAdminEmail}, nil
	}
	if err != nil {
		return BootstrapStatus{}, fmt.Errorf("read bootstrap state: %w", err)
	}
	status.Required = hash == ""
	if status.Required {
		status.Email = BootstrapAdminEmail
	}
	return status, nil
}

// CompleteBootstrap sets the bootstrap admin password exactly once. It
// validates the password policy, then conditionally fills the empty hash and
// clears must_change_password. A non-empty hash (a completed bootstrap, or an
// admin provisioned with a developer password by `seed`) makes it fail with
// ErrBootstrapComplete. On success it issues a token pair so the first admin
// is logged straight into the console.
func (s *Service) CompleteBootstrap(ctx context.Context, password, ip, userAgent string) (TokenPair, error) {
	if err := ValidatePassword(password, 10); err != nil {
		return TokenPair{}, err
	}
	hash, err := hashPassword(password, s.tune.PasswordPolicy)
	if err != nil {
		return TokenPair{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("begin bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID string
	err = tx.QueryRow(ctx,
		`UPDATE app_user
		 SET password_hash = $2,
		     must_change_password = FALSE,
		     email_verified = TRUE,
		     updated_at = now()
		 WHERE email = $1 AND tenant_id IS NULL AND password_hash = ''
		 RETURNING id::text`,
		BootstrapAdminEmail, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrBootstrapComplete
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("set bootstrap password: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenPair{}, fmt.Errorf("commit bootstrap: %w", err)
	}

	row, err := findUserByID(ctx, s.pool, userID)
	if err != nil {
		return TokenPair{}, err
	}
	pair, err := s.issuePair(ctx, row, ip, userAgent)
	if err != nil {
		return TokenPair{}, err
	}
	s.recordAudit(ctx, audit.Input{
		ActorID:      row.ID,
		ActorName:    row.Email,
		EventType:    audit.EventPermission,
		Action:       "bootstrap",
		ResourceType: "app_user",
		ResourceID:   row.ID,
		IP:           ip,
	})
	return pair, nil
}
