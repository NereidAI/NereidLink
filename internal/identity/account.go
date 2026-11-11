// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// account.go provides the administrator-facing account mutations used
// by the platform user-management service: credential reset and the
// enable/disable switch. Both keep the login security state coherent
// by clearing the failure/lock window alongside the change.

package identity

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ResetPassword validates newPassword against policy, replaces the
// stored hash and clears any failure/lock state so the account can
// log in immediately with the new credential.
func ResetPassword(ctx context.Context, pool *pgxpool.Pool,
	policy PasswordPolicy, userID, newPassword string) error {
	if err := ValidatePassword(newPassword, 10); err != nil {
		return err
	}
	hash, err := hashPassword(newPassword, policy)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx,
		`UPDATE app_user
		 SET password_hash = $1,
		     failed_login_count = 0,
		     first_failed_at = NULL,
		     locked_until = NULL,
		     updated_at = now()
		 WHERE id = $2`, hash, userID)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetAccountDisabled flips the administrative enable switch. A
// disabled account is rejected at login and at token verification;
// existing access tokens stop working on the next request.
func SetAccountDisabled(ctx context.Context, pool *pgxpool.Pool,
	userID string, disabled bool) error {
	tag, err := pool.Exec(ctx,
		`UPDATE app_user
		 SET disabled = $1, updated_at = now()
		 WHERE id = $2`, disabled, userID)
	if err != nil {
		return fmt.Errorf("set account disabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
