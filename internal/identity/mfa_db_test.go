// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// mfa_db_test.go covers TOTP enrollment, MFA challenge and recovery-code
// login against an opt-in PostgreSQL database.

package identity

import (
	"context"
	"encoding/base32"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mfaTestPool connects when NL_TEST_DATABASE_URL is configured.
func mfaTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedMFAUser creates disposable tenant and user rows.
func seedMFAUser(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	tenantID := "00000000-0000-0000-0000-00000000b701"
	userID := "00000000-0000-0000-0000-00000000b801"
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'mfa-test')
		 ON CONFLICT (id) DO NOTHING`, tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user
		   (id, tenant_id, email, display_name, password_hash, email_verified)
		 VALUES ($1, $2, 'mfa@example.test', 'MFA User', 'phc', true)
		 ON CONFLICT (id) DO UPDATE
		 SET tenant_id = EXCLUDED.tenant_id, email = EXCLUDED.email,
		     display_name = EXCLUDED.display_name, disabled = false`,
		userID, tenantID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM app_user WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM tenant WHERE id = $1`, tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})
	return tenantID, userID
}

// TestTOTPEnrollmentAndMFA verifies setup, confirmation, status, TOTP login
// and recovery-code login.
func TestTOTPEnrollmentAndMFA(t *testing.T) {
	pool := mfaTestPool(t)
	_, userID := seedMFAUser(t, pool)
	svc, err := NewService(pool, "0123456789abcdef", DefaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	enrollment, err := svc.StartTOTP(ctx, userID)
	if err != nil {
		t.Fatalf("start totp: %v", err)
	}
	secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	code, err := totpCode(secretBytes, time.Now())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	recovery, err := svc.ConfirmTOTP(ctx, userID, code)
	if err != nil {
		t.Fatalf("confirm totp: %v", err)
	}
	if len(recovery.Codes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(recovery.Codes))
	}
	status, err := svc.MFAStatus(ctx, userID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.TOTPEnabled {
		t.Fatal("TOTP not enabled after confirmation")
	}

	row, err := findUserByID(ctx, pool, userID)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	challenge, err := svc.createMFAChallenge(row)
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	currentCode, err := totpCode(secretBytes, time.Now())
	if err != nil {
		t.Fatalf("second code: %v", err)
	}
	pair, err := svc.VerifyMFAChallenge(ctx, challenge.ChallengeToken, currentCode, "ip", "agent")
	if err != nil {
		t.Fatalf("verify totp challenge: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("verified challenge did not issue a token pair")
	}
	if _, err := svc.Current(ctx, challenge.ChallengeToken); err == nil {
		t.Fatal("challenge token accepted as an access token")
	}
	if _, err := svc.ParseAccessToken(ctx, challenge.ChallengeToken); err == nil {
		t.Fatal("challenge token accepted by access-token parser")
	}

	secondRow, err := findUserByID(ctx, pool, userID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	secondChallenge, err := svc.createMFAChallenge(secondRow)
	if err != nil {
		t.Fatalf("second challenge: %v", err)
	}
	recoveryPair, err := svc.VerifyMFAChallenge(ctx, secondChallenge.ChallengeToken, recovery.Codes[0], "ip", "agent")
	if err != nil {
		t.Fatalf("verify recovery code: %v", err)
	}
	if recoveryPair.AccessToken == "" {
		t.Fatal("recovery code did not issue a token pair")
	}
}

// TestVerifyMFAChallengeRejectsBadCode verifies second-factor failure.
func TestVerifyMFAChallengeRejectsBadCode(t *testing.T) {
	pool := mfaTestPool(t)
	_, userID := seedMFAUser(t, pool)
	svc, err := NewService(pool, "0123456789abcdef", DefaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	enrollment, err := svc.StartTOTP(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	secretBytes, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	code, _ := totpCode(secretBytes, time.Now())
	if _, err := svc.ConfirmTOTP(ctx, userID, code); err != nil {
		t.Fatal(err)
	}
	row, _ := findUserByID(ctx, pool, userID)
	challenge, _ := svc.createMFAChallenge(row)
	if _, err := svc.VerifyMFAChallenge(ctx, challenge.ChallengeToken, "000000", "ip", "agent"); err == nil {
		t.Fatal("bad second factor accepted")
	}
}

// TestDisableTOTP verifies state removal.
func TestDisableTOTP(t *testing.T) {
	pool := mfaTestPool(t)
	_, userID := seedMFAUser(t, pool)
	svc, _ := NewService(pool, "0123456789abcdef", DefaultTuning())
	ctx := context.Background()
	enrollment, _ := svc.StartTOTP(ctx, userID)
	secretBytes, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	code, _ := totpCode(secretBytes, time.Now())
	if _, err := svc.ConfirmTOTP(ctx, userID, code); err != nil {
		t.Fatal(err)
	}
	if err := svc.DisableTOTP(ctx, userID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	status, _ := svc.MFAStatus(ctx, userID)
	if status.TOTPEnabled {
		t.Fatal("TOTP still enabled")
	}
}
