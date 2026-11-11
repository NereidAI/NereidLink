// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// mfa.go orchestrates TOTP enrollment, recovery codes and the login challenge
// required by ADR-0021.
//
// A password alone proves only one factor. When TOTP is enabled, Login stops
// after password verification and returns a short challenge token; the actual
// session and access token are created only after a TOTP or recovery code is
// verified. Keeping issuance behind that second verification is the invariant
// that prevents a stolen password from becoming a complete login.

package identity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	mfaChallengeTTL     = 5 * time.Minute
	mfaPurposeChallenge = "mfa_challenge"
	recoveryCodeCount   = 10
	recoveryCodeLength  = 10
)

// MFAChallenge tells the client a second factor is required.
type MFAChallenge struct {
	// ChallengeToken is exchanged with a TOTP or recovery code.
	ChallengeToken string `json:"challenge_token"`

	// ExpiresIn is the challenge lifetime in seconds.
	ExpiresIn int `json:"expires_in"`
}

// LoginResult is either a token pair or an MFA challenge.
type LoginResult struct {
	// MFA is populated when a second factor is required.
	MFA *MFAChallenge `json:"mfa,omitempty"`

	// Pair is populated when no second factor is required.
	Pair *TokenPair `json:"pair,omitempty"`
}

// RecoveryCodeSet is shown once immediately after TOTP confirmation.
type RecoveryCodeSet struct {
	// Codes are single-use recovery codes.
	Codes []string `json:"codes"`
}

// MFAStatus describes the current user's MFA enrollment.
type MFAStatus struct {
	// TOTPEnabled reports active enrollment.
	TOTPEnabled bool `json:"totp_enabled"`
}

// StartTOTP begins enrollment by storing a pending encrypted secret.
func (s *Service) StartTOTP(ctx context.Context, userID string) (TOTPEnrollment, error) {
	encoded, raw, err := generateTOTPSecret()
	if err != nil {
		return TOTPEnrollment{}, err
	}
	cipherText, err := s.encryptMFASecret(raw)
	if err != nil {
		return TOTPEnrollment{}, err
	}

	var email string
	if err := s.pool.QueryRow(ctx,
		`UPDATE app_user
		 SET totp_pending_secret_cipher = $2, updated_at = now()
		 WHERE id = $1
		 RETURNING email`, userID, cipherText).Scan(&email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TOTPEnrollment{}, ErrUserNotFound
		}
		return TOTPEnrollment{}, fmt.Errorf("store pending totp secret: %w", err)
	}

	return TOTPEnrollment{Secret: encoded, URI: totpURI(email, encoded)}, nil
}

// ConfirmTOTP verifies possession of the pending secret and activates it.
func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string) (RecoveryCodeSet, error) {
	var pendingCipher *string
	if err := s.pool.QueryRow(ctx,
		`SELECT totp_pending_secret_cipher FROM app_user WHERE id = $1`,
		userID).Scan(&pendingCipher); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RecoveryCodeSet{}, ErrUserNotFound
		}
		return RecoveryCodeSet{}, fmt.Errorf("load pending totp secret: %w", err)
	}
	if pendingCipher == nil {
		return RecoveryCodeSet{}, fmt.Errorf("%w: no pending TOTP enrollment", ErrInvalidArgument)
	}

	secret, err := s.decryptMFASecret(*pendingCipher)
	if err != nil {
		return RecoveryCodeSet{}, err
	}
	if !validateTOTPCode(secret, strings.TrimSpace(code), time.Now()) {
		return RecoveryCodeSet{}, fmt.Errorf("%w: invalid TOTP code", ErrInvalidArgument)
	}

	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return RecoveryCodeSet{}, err
	}
	activeCipher, err := s.encryptMFASecret(secret)
	if err != nil {
		return RecoveryCodeSet{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecoveryCodeSet{}, fmt.Errorf("begin totp confirmation: %w", err)
	}
	commitErr := func() error {
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx,
			`UPDATE app_user
			 SET totp_secret_cipher = $2,
			     totp_pending_secret_cipher = NULL,
			     totp_enabled_at = now(),
			     updated_at = now()
			 WHERE id = $1`, userID, activeCipher); err != nil {
			return fmt.Errorf("activate totp secret: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM mfa_recovery_code WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("replace recovery codes: %w", err)
		}
		for _, hash := range hashes {
			if _, err := tx.Exec(ctx,
				`INSERT INTO mfa_recovery_code (user_id, code_hash)
				 VALUES ($1, $2)`, userID, hash); err != nil {
				return fmt.Errorf("store recovery code: %w", err)
			}
		}
		return tx.Commit(ctx)
	}()
	if commitErr != nil {
		return RecoveryCodeSet{}, commitErr
	}
	return RecoveryCodeSet{Codes: codes}, nil
}

// DisableTOTP removes MFA state for the authenticated user.
func (s *Service) DisableTOTP(ctx context.Context, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_user
		 SET totp_secret_cipher = NULL,
		     totp_pending_secret_cipher = NULL,
		     totp_enabled_at = NULL,
		     updated_at = now()
		 WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	_, err = s.pool.Exec(ctx,
		`DELETE FROM mfa_recovery_code WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	return nil
}

// MFAStatus returns the current user's MFA state.
func (s *Service) MFAStatus(ctx context.Context, userID string) (MFAStatus, error) {
	var enabled bool
	if err := s.pool.QueryRow(ctx,
		`SELECT totp_enabled_at IS NOT NULL FROM app_user WHERE id = $1`,
		userID).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MFAStatus{}, ErrUserNotFound
		}
		return MFAStatus{}, fmt.Errorf("load mfa status: %w", err)
	}
	return MFAStatus{TOTPEnabled: enabled}, nil
}

// createMFAChallenge issues a short token proving password verification.
func (s *Service) createMFAChallenge(row userRow) (MFAChallenge, error) {
	now := time.Now()
	token, err := s.signer.issue(Claims{
		Subject:       row.ID,
		TenantID:      row.TenantID,
		Email:         row.Email,
		PlatformAdmin: row.PlatformAdmin,
		SessionID:     "",
		Purpose:       mfaPurposeChallenge,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(mfaChallengeTTL).Unix(),
	})
	if err != nil {
		return MFAChallenge{}, err
	}
	return MFAChallenge{ChallengeToken: token, ExpiresIn: int(mfaChallengeTTL.Seconds())}, nil
}

// VerifyMFAChallenge validates a second factor and then issues a full pair.
func (s *Service) VerifyMFAChallenge(ctx context.Context, challengeToken, code, ip, userAgent string) (TokenPair, error) {
	claims, err := s.signer.parse(challengeToken)
	if err != nil {
		return TokenPair{}, ErrInvalidToken
	}
	if claims.Purpose != mfaPurposeChallenge {
		return TokenPair{}, ErrInvalidToken
	}

	row, err := findUserByID(ctx, s.pool, claims.Subject)
	if err != nil {
		return TokenPair{}, err
	}
	if row.Disabled {
		return TokenPair{}, ErrUserDisabled
	}
	if !s.verifySecondFactor(ctx, row, strings.TrimSpace(code)) {
		return TokenPair{}, fmt.Errorf("%w: invalid second factor", ErrInvalidCredentials)
	}
	return s.issuePair(ctx, row, ip, userAgent)
}

// verifySecondFactor accepts TOTP or a single-use recovery code.
func (s *Service) verifySecondFactor(ctx context.Context, row userRow, code string) bool {
	var secretCipher *string
	if err := s.pool.QueryRow(ctx,
		`SELECT totp_secret_cipher FROM app_user WHERE id = $1`, row.ID,
	).Scan(&secretCipher); err != nil || secretCipher == nil {
		return false
	}
	secret, err := s.decryptMFASecret(*secretCipher)
	if err == nil && validateTOTPCode(secret, code, time.Now()) {
		return true
	}
	return consumeRecoveryCode(ctx, s.pool, row.ID, code)
}

// encryptMFASecret seals a TOTP secret with an AES-GCM key derived from the
// signing secret. The key purpose prefix keeps it distinct from the JWT HMAC
// use even though both originate from the same configured value.
func (s *Service) encryptMFASecret(plain []byte) (string, error) {
	block, err := aes.NewCipher(s.mfaKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// decryptMFASecret reverses encryptMFASecret.
func (s *Service) decryptMFASecret(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode mfa secret: %w", err)
	}
	block, err := aes.NewCipher(s.mfaKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("encrypted mfa secret is too short")
	}
	nonce, cipherText := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt mfa secret: %w", err)
	}
	return plain, nil
}

// mfaKey derives the 32-byte encryption key.
func (s *Service) mfaKey() []byte {
	sum := sha256.Sum256(append([]byte("nereidlink-mfa-secret-v1:"), s.signer.secret...))
	return sum[:]
}

// generateRecoveryCodes creates high-entropy single-use codes and hashes.
func generateRecoveryCodes() ([]string, []string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		raw := make([]byte, recoveryCodeLength)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		code := make([]byte, recoveryCodeLength)
		for i := range raw {
			code[i] = alphabet[int(raw[i])%len(alphabet)]
		}
		value := string(code)
		codes = append(codes, value)
		sum := sha256.Sum256([]byte(value))
		hashes = append(hashes, base64.RawURLEncoding.EncodeToString(sum[:]))
	}
	return codes, hashes, nil
}

// consumeRecoveryCode marks one unused recovery code consumed.
func consumeRecoveryCode(ctx context.Context, pool *pgxpool.Pool, userID, code string) bool {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	hash := base64.RawURLEncoding.EncodeToString(sum[:])
	tag, err := pool.Exec(ctx,
		`UPDATE mfa_recovery_code
		 SET used_at = now()
		 WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		userID, hash)
	if err != nil {
		return false
	}
	return tag.RowsAffected() > 0
}
