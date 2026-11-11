// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// session_db_test.go verifies ADR-0021 session listing, owner-scoped
// revocation and immediate access-token invalidation against an opt-in
// PostgreSQL database.

package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	sessTestTenant = "00000000-0000-0000-0000-00000000b901"
	sessUserA      = "00000000-0000-0000-0000-00000000b911"
	sessUserB      = "00000000-0000-0000-0000-00000000b912"
)

// issueSessionToken mints a stored session and its matching access token.
func issueSessionToken(t *testing.T, svc *Service, userID, ip string) (token, sid string) {
	t.Helper()
	ctx := context.Background()
	_, hash, err := newRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	row, err := createSession(ctx, svc.pool, userID, hash, ip, "pytest-agent", svc.tune.RefreshTTL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, err := svc.signer.issue(Claims{
		Subject:   userID,
		Email:     userID + "@example.test",
		SessionID: row.ID,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(svc.tune.AccessTTL).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok, row.ID
}

// TestSessionListRevokeAndInvalidation covers listing with the current
// marker, self revocation, immediate token rejection and cross-user safety.
func TestSessionListRevokeAndInvalidation(t *testing.T) {
	pool := mfaTestPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'session-test')
		 ON CONFLICT (id) DO NOTHING`, sessTestTenant); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, email string }{
		{sessUserA, "sess-a@example.test"},
		{sessUserB, "sess-b@example.test"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO app_user (id, tenant_id, email, display_name, password_hash, email_verified)
			 VALUES ($1, $2, $3, 'Session', 'phc', true)
			 ON CONFLICT (id) DO UPDATE SET disabled = false`,
			u.id, sessTestTenant, u.email); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, id := range []string{sessUserA, sessUserB} {
			_, _ = pool.Exec(c, `DELETE FROM app_user WHERE id = $1`, id)
		}
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id = $1`, sessTestTenant)
	})

	svc, err := NewService(pool, "0123456789abcdef", DefaultTuning())
	if err != nil {
		t.Fatal(err)
	}

	tokenA, sidA := issueSessionToken(t, svc, sessUserA, "10.0.0.1")
	_, sidB := issueSessionToken(t, svc, sessUserB, "10.0.0.2")

	// The freshly issued token validates and its session is active.
	if _, err := svc.ParseAccessToken(ctx, tokenA); err != nil {
		t.Fatalf("new token must validate: %v", err)
	}

	sessions, err := svc.ListSessions(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != sidA || !sessions[0].Current || !sessions[0].Active {
		t.Fatalf("unexpected session list: %+v", sessions)
	}
	if sessions[0].IP != "10.0.0.1" {
		t.Fatalf("session ip not recorded: %+v", sessions[0])
	}

	// A user cannot revoke another user's session by id.
	if _, err := svc.RevokeSession(ctx, tokenA, sidB); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-user revoke must be ErrSessionNotFound, got %v", err)
	}

	// Revoking the current session reports current=true.
	current, err := svc.RevokeSession(ctx, tokenA, sidA)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("revoking the token's own session must report current=true")
	}

	// Revocation is effective immediately: the still-signed access token
	// no longer resolves, and Current() rejects it as well.
	if _, err := svc.ParseAccessToken(ctx, tokenA); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token must be ErrInvalidToken, got %v", err)
	}
	if _, err := svc.Current(ctx, tokenA); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token Current() must be ErrInvalidToken, got %v", err)
	}

	// Revoking again is idempotent (owned row exists, already revoked).
	if _, err := svc.RevokeSession(ctx, tokenA, sidA); err != nil {
		t.Fatalf("repeat revoke must be idempotent, got %v", err)
	}
}
