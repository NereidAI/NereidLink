// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stepup.go exposes an operation-time TOTP check for the step-up layer
// (ADR-0035). Unlike the login second-factor path it accepts only a current
// TOTP code — recovery codes never authorize an immediate dangerous action —
// and reuses the account's existing login-failure lockout counters so a
// guessed code is throttled exactly like a guessed password.

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrTOTPNotEnrolled means the user has no active TOTP enrollment.
	ErrTOTPNotEnrolled = errors.New("TOTP is not enrolled for this user")

	// ErrInvalidTOTP means the presented code did not match.
	ErrInvalidTOTP = errors.New("invalid TOTP code")
)

// VerifyTOTP validates a current 6-digit TOTP code for userID. A wrong code
// is counted in the login-failure window and can lock the account; a
// successful check resets the counter.
func (s *Service) VerifyTOTP(ctx context.Context, userID, code string) error {
	row, err := findUserByID(ctx, s.pool, userID)
	if err != nil {
		return err
	}
	if row.Disabled {
		return ErrUserDisabled
	}
	if row.LockedUntil != nil && row.LockedUntil.After(time.Now()) {
		return ErrUserLocked
	}

	var secretCipher *string
	if err := s.pool.QueryRow(ctx,
		`SELECT totp_secret_cipher FROM app_user WHERE id = $1`,
		userID).Scan(&secretCipher); err != nil {
		return fmt.Errorf("load totp secret: %w", err)
	}
	if secretCipher == nil {
		return ErrTOTPNotEnrolled
	}

	secret, err := s.decryptMFASecret(*secretCipher)
	if err != nil {
		return err
	}
	if !validateTOTPCode(secret, strings.TrimSpace(code), time.Now()) {
		if err := recordLoginFailure(ctx, s.pool, row); err != nil {
			return err
		}
		return ErrInvalidTOTP
	}
	return resetLoginFailure(ctx, s.pool, row.ID)
}
