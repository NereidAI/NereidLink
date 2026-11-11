// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// session.go persists refresh-token sessions. Only the SHA-256 hash of
// a refresh token is stored (ADR-0021: session records never store the
// token itself), so a database compromise does not yield usable
// sessions. Refresh rotates: each use revokes the old session and
// creates a new one.

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionRow is the stored projection of one refresh session.
type sessionRow struct {
	// ID identifies the session and anchors the access-token sid.
	ID string

	// UserID owns the session.
	UserID string

	// ExpiresAt is the refresh-token TTL deadline.
	ExpiresAt time.Time

	// RevokedAt marks explicit logout or rotation.
	RevokedAt *time.Time
}

// newRefreshToken returns a 256-bit opaque refresh token, base64url
// encoded for transport.
func newRefreshToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), hashRefreshToken(raw), nil
}

// hashRefreshToken returns the stored SHA-256 digest of raw token bytes.
func hashRefreshToken(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

// hashRefreshTokenString hashes a transported base64 token back to its
// stored digest.
func hashRefreshTokenString(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errors.New("malformed refresh token")
	}
	return hashRefreshToken(raw), nil
}

// createSession inserts one session row.
func createSession(ctx context.Context, pool *pgxpool.Pool,
	userID string, tokenHash []byte, ip, userAgent string,
	ttl time.Duration) (sessionRow, error) {
	id := uuid.NewString()
	expiresAt := time.Now().Add(ttl)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_session
		   (id, user_id, refresh_token_hash, ip, user_agent, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, userID, tokenHash, clipText(ip, 64), clipText(userAgent, 256), expiresAt); err != nil {
		return sessionRow{}, fmt.Errorf("create session: %w", err)
	}
	return sessionRow{ID: id, UserID: userID, ExpiresAt: expiresAt}, nil
}

// findSession loads one session by token hash.
func findSession(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte) (sessionRow, error) {
	var session sessionRow
	var expiresAt time.Time
	var revokedAt *time.Time

	err := pool.QueryRow(ctx,
		`SELECT id::text, user_id::text, expires_at, revoked_at
		 FROM user_session
		 WHERE refresh_token_hash = $1`,
		tokenHash).Scan(&session.ID, &session.UserID, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionRow{}, ErrSessionNotFound
	}
	if err != nil {
		return sessionRow{}, fmt.Errorf("find session: %w", err)
	}
	session.ExpiresAt = expiresAt
	session.RevokedAt = revokedAt
	return session, nil
}

// revokeSession marks one session revoked. Revoking an already revoked
// session is not an error (logout is idempotent).
func revokeSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE user_session SET revoked_at = now()
		 WHERE id = $1 AND revoked_at IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	_ = tag
	return nil
}

// clipText bounds metadata stored from request-supplied values so a
// long header cannot bloat the table.
func clipText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// SessionInfo is the owner-facing projection of one session. The
// refresh-token hash is never exposed.
type SessionInfo struct {
	// ID is the session identifier (the access-token `sid` claim).
	ID string `json:"id"`

	// IP is the address recorded when the session was created.
	IP string `json:"ip"`

	// UserAgent is the client string recorded at creation.
	UserAgent string `json:"user_agent"`

	// Current is true for the session backing the requesting token.
	Current bool `json:"current"`

	// Active is true when not revoked and not past its refresh TTL.
	Active bool `json:"active"`

	// CreatedAt is when the session (and its refresh token) was minted.
	CreatedAt time.Time `json:"created_at"`

	// ExpiresAt is the refresh-token TTL deadline.
	ExpiresAt time.Time `json:"expires_at"`
}

// listUserSessions returns the user's currently usable sessions
// (not revoked, inside refresh TTL), newest first. Revoked/expired
// history belongs in the audit log and is intentionally omitted so the
// list stays a short "where am I logged in" view rather than growing
// without bound for long-lived accounts.
func listUserSessions(ctx context.Context, pool *pgxpool.Pool,
	userID, currentSessionID string) ([]SessionInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, ip, user_agent, created_at, expires_at, TRUE AS active
		 FROM user_session
		 WHERE user_id = $1
		   AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY created_at DESC, id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.ID, &info.IP, &info.UserAgent,
			&info.CreatedAt, &info.ExpiresAt, &info.Active); err != nil {
			return nil, err
		}
		info.Current = info.ID == currentSessionID
		out = append(out, info)
	}
	return out, rows.Err()
}

// revokeUserSession marks one session revoked, scoped to its owner so a
// caller can never revoke another user's session by id. found reports
// whether an owned row exists (even if it was already revoked); changed
// reports whether a previously-live row was flipped.
func revokeUserSession(ctx context.Context, pool *pgxpool.Pool,
	userID, sessionID string) (found, changed bool, err error) {
	tag, err := pool.Exec(ctx,
		`UPDATE user_session SET revoked_at = now()
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		sessionID, userID)
	if err != nil {
		return false, false, fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, true, nil
	}
	// No live row flipped: distinguish "already revoked but owned"
	// (idempotent success) from "foreign/unknown id" (not found).
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_session WHERE id = $1 AND user_id = $2)`,
		sessionID, userID).Scan(&found); err != nil {
		return false, false, fmt.Errorf("locate session: %w", err)
	}
	return found, false, nil
}

// sessionActive reports whether the session backing an access token is
// still usable: present, not revoked and inside its refresh TTL. A
// missing session id (legacy/non-session token) is treated as active so
// tokens minted before session tracking are not locked out; every token
// the local fallback issues carries a sid.
func sessionActive(ctx context.Context, pool *pgxpool.Pool, sessionID string) (bool, error) {
	if sessionID == "" {
		return true, nil
	}
	var active bool
	err := pool.QueryRow(ctx,
		`SELECT (revoked_at IS NULL AND expires_at > now())
		 FROM user_session WHERE id = $1`, sessionID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	return active, nil
}
