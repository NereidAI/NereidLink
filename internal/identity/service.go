// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the identity facade used by the REST layer: login,
// refresh rotation, logout and current-user resolution. All credential
// and session state changes pass through here so the lockout, rotation
// and revocation invariants cannot be bypassed by callers.

package identity

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/audit"
)

// Tuning configures token lifetimes and password costs.
type Tuning struct {
	// AccessTTL is the short access-token lifetime; ADR default 30m.
	AccessTTL time.Duration

	// RefreshTTL is the session lifetime; ADR default 7d.
	RefreshTTL time.Duration

	// PasswordPolicy sets argon2id costs.
	PasswordPolicy PasswordPolicy
}

// DefaultTuning returns the ADR defaults.
func DefaultTuning() Tuning {
	return Tuning{
		AccessTTL:      30 * time.Minute,
		RefreshTTL:     7 * 24 * time.Hour,
		PasswordPolicy: DefaultPasswordPolicy(),
	}
}

// TokenPair is a successful login/refresh response.
type TokenPair struct {
	// AccessToken is the bearer token presented on REST and WS.
	AccessToken string `json:"access_token"`

	// RefreshToken is the opaque rotation secret.
	RefreshToken string `json:"refresh_token"`

	// TokenType is always "Bearer".
	TokenType string `json:"token_type"`

	// ExpiresIn is the access-token lifetime in seconds.
	ExpiresIn int `json:"expires_in"`

	// User is the authenticated account.
	User User `json:"user"`
}

// Service is the identity facade.
type Service struct {
	pool   *pgxpool.Pool
	signer *signer
	tune   Tuning

	// audit records authentication events; nil is tolerated in tests.
	audit *audit.Service
}

// SetAuditService wires the append-only audit log. A failure to
// audit is logged as an alert but does not block the authentication
// operation.
func (s *Service) SetAuditService(service *audit.Service) {
	s.audit = service
}

// recordAudit appends an event when auditing is wired, surfacing
// write failures loudly per ADR-0029 (audit must never silently lose
// an event).
func (s *Service) recordAudit(ctx context.Context, event audit.Input) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Append(ctx, event); err != nil {
		slog.Error("failed to write audit event", "error", err, "type", event.EventType)
	}
}

// NewService constructs the service with the operator signing secret.
func NewService(pool *pgxpool.Pool, secret string, tune Tuning) (*Service, error) {
	signer, err := newSigner(secret)
	if err != nil {
		return nil, err
	}
	if tune.AccessTTL <= 0 || tune.RefreshTTL <= 0 {
		tune = DefaultTuning()
	}
	return &Service{pool: pool, signer: signer, tune: tune}, nil
}

// Login authenticates email + password and issues a token pair tied to
// a new session. Wrong identifiers and wrong passwords return the same
// error to prevent account enumeration; disabled and locked states are
// reported distinctly so operators can act.
func (s *Service) Login(ctx context.Context, email, password, ip, userAgent string) (LoginResult, error) {
	// Emails resolve globally: tenant users are scoped by membership,
	// not by a tenant field in the login form. Uniqueness is enforced
	// at account creation; duplicate legacy rows must disambiguate.
	matches, err := findUsersByEmailGlobal(ctx, s.pool, normalizeEmail(email))
	if err != nil {
		return LoginResult{}, err
	}
	if len(matches) == 0 {
		s.recordAudit(ctx, audit.Input{
			ActorName: normalizeEmail(email), EventType: audit.EventAuthLoginFailed,
			Action: "login", IP: ip,
		})
		return LoginResult{}, ErrInvalidCredentials
	}
	if len(matches) > 1 {
		return LoginResult{}, ErrLoginAmbiguous
	}
	row := matches[0]
	if row.Disabled {
		return LoginResult{}, ErrUserDisabled
	}
	if row.LockedUntil != nil && row.LockedUntil.After(time.Now()) {
		return LoginResult{}, ErrUserLocked
	}

	// Before first-run bootstrap the admin hash is empty. Do not treat a
	// login attempt as a failed password (no lockout increment); the caller
	// is directed to the setup screen by GET /v1/auth/bootstrap-status.
	if row.PasswordHash == "" {
		s.recordAudit(ctx, audit.Input{
			ActorName: row.Email,
			EventType: audit.EventAuthLoginFailed,
			Action:    "login",
			IP:        ip,
		})
		return LoginResult{}, ErrPasswordNotSet
	}

	ok, err := verifyPassword(password, row.PasswordHash)
	if err != nil || !ok {
		if recordErr := recordLoginFailure(ctx, s.pool, row); recordErr != nil {
			return LoginResult{}, recordErr
		}
		s.recordAudit(ctx, audit.Input{
			ActorID: row.ID, ActorName: row.Email,
			EventType: audit.EventAuthLoginFailed, Action: "login", IP: ip,
		})
		return LoginResult{}, ErrInvalidCredentials
	}

	if err := resetLoginFailure(ctx, s.pool, row.ID); err != nil {
		return LoginResult{}, err
	}

	var mfaEnabled bool
	if err := s.pool.QueryRow(ctx,
		`SELECT totp_enabled_at IS NOT NULL FROM app_user WHERE id = $1`,
		row.ID).Scan(&mfaEnabled); err != nil {
		return LoginResult{}, fmt.Errorf("load mfa state: %w", err)
	}
	if mfaEnabled {
		challenge, err := s.createMFAChallenge(row)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFA: &challenge}, nil
	}

	pair, err := s.issuePair(ctx, row, ip, userAgent)
	if err != nil {
		return LoginResult{}, err
	}
	s.recordAudit(ctx, audit.Input{
		TenantID: row.TenantID, ActorID: row.ID, ActorName: row.Email,
		EventType: audit.EventAuthLogin, Action: "login",
		ResourceType: "app_user", ResourceID: row.ID, IP: ip,
	})
	return LoginResult{Pair: &pair}, nil
}

// Refresh validates a refresh token, rotates its session and returns a
// new pair. The presented session is revoked so a reused token fails:
// rotation both refreshes credentials and detects replay.
func (s *Service) Refresh(ctx context.Context, refreshToken, ip, userAgent string) (TokenPair, error) {
	tokenHash, err := hashRefreshTokenString(refreshToken)
	if err != nil {
		return TokenPair{}, ErrSessionNotFound
	}

	session, err := findSession(ctx, s.pool, tokenHash)
	if err != nil {
		return TokenPair{}, err
	}
	switch {
	case session.RevokedAt != nil:
		return TokenPair{}, ErrSessionRevoked
	case session.ExpiresAt.Before(time.Now()):
		return TokenPair{}, ErrSessionExpired
	}

	row, err := findUserByID(ctx, s.pool, session.UserID)
	if err != nil {
		return TokenPair{}, err
	}
	if row.Disabled {
		return TokenPair{}, ErrUserDisabled
	}

	if err := revokeSession(ctx, s.pool, session.ID); err != nil {
		return TokenPair{}, err
	}
	return s.issuePair(ctx, row, ip, userAgent)
}

// Logout revokes the session bound to an access token. An invalid
// token is tolerated so logout always succeeds from the client's
// perspective.
func (s *Service) Logout(ctx context.Context, accessToken string) error {
	claims, err := s.signer.parse(accessToken)
	if err != nil {
		return nil
	}
	s.recordAudit(ctx, audit.Input{
		ActorID: claims.Subject, ActorName: claims.Email,
		EventType: audit.EventAuthLogout, Action: "logout",
		ResourceType: "app_user", ResourceID: claims.Subject,
	})
	return revokeSession(ctx, s.pool, claims.SessionID)
}

// Current resolves the user bound to a verified access token.
func (s *Service) Current(ctx context.Context, accessToken string) (User, error) {
	claims, err := s.signer.parse(accessToken)
	if err != nil {
		return User{}, err
	}
	if claims.Purpose != "" {
		return User{}, ErrInvalidToken
	}
	active, err := sessionActive(ctx, s.pool, claims.SessionID)
	if err != nil {
		return User{}, err
	}
	if !active {
		return User{}, fmt.Errorf("%w: session revoked", ErrInvalidToken)
	}
	row, err := findUserByID(ctx, s.pool, claims.Subject)
	if err != nil {
		return User{}, err
	}
	if row.Disabled {
		return User{}, ErrUserDisabled
	}
	user := row.User
	if err := attachTenantRoles(ctx, s.pool, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// ParseAccessToken verifies an access token, confirms its backing
// session is still active and returns the claims. It is the shared
// authentication entry for non-HTTP entry points (the WebSocket
// handshake) and the RBAC resolver. Checking the session here makes
// logout/revoke effective immediately instead of only after the short
// access-token TTL: a revoked or expired session fails closed even
// though the HMAC is still valid.
func (s *Service) ParseAccessToken(ctx context.Context, accessToken string) (Claims, error) {
	claims, err := s.signer.parse(accessToken)
	if err != nil {
		return Claims{}, err
	}
	if claims.Purpose != "" {
		return Claims{}, ErrInvalidToken
	}
	// A machine token authenticates a service account and has no refresh
	// session: its HMAC validity and expiry ARE its entire lifecycle. It
	// must not be sent through the human session lookup, where an empty
	// SessionID would be treated as a revoked session and fail closed.
	if claims.Machine {
		return claims, nil
	}
	active, err := sessionActive(ctx, s.pool, claims.SessionID)
	if err != nil {
		return Claims{}, err
	}
	if !active {
		return Claims{}, fmt.Errorf("%w: session revoked", ErrInvalidToken)
	}
	return claims, nil
}

// ListSessions returns the caller's refresh sessions, marking the one
// backing the supplied access token as current.
func (s *Service) ListSessions(ctx context.Context, accessToken string) ([]SessionInfo, error) {
	claims, err := s.signer.parse(accessToken)
	if err != nil {
		return nil, err
	}
	if claims.Purpose != "" {
		return nil, ErrInvalidToken
	}
	return listUserSessions(ctx, s.pool, claims.Subject, claims.SessionID)
}

// RevokeSession revokes one of the caller's own sessions by id. It
// returns true when the revoked session is the one backing the supplied
// access token (so the API can hint that the current client is logged
// out). Revoking a foreign session id is reported as not found.
func (s *Service) RevokeSession(ctx context.Context, accessToken, sessionID string) (bool, error) {
	claims, err := s.signer.parse(accessToken)
	if err != nil {
		return false, err
	}
	if claims.Purpose != "" {
		return false, ErrInvalidToken
	}
	found, _, err := revokeUserSession(ctx, s.pool, claims.Subject, sessionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrSessionNotFound
	}
	s.recordAudit(ctx, audit.Input{
		ActorID: claims.Subject, ActorName: claims.Email,
		EventType: audit.EventAuthLogout, Action: "revoke-session",
		ResourceType: "user_session", ResourceID: sessionID,
	})
	return sessionID == claims.SessionID, nil
}

// IssueMachineAccessToken signs a short-lived access token for an OAuth2
// client_credentials service account (ADR-0021). Unlike issuePair it
// creates no refresh session: a machine secret is presented afresh on
// every grant, so there is nothing to rotate or revoke per token. The
// returned expiry matches the standard access-token TTL.
func (s *Service) IssueMachineAccessToken(subject, tenantID, clientID string,
	scopes []string) (string, time.Time, error) {
	now := time.Now()
	expiry := now.Add(s.tune.AccessTTL)
	token, err := s.signer.issue(Claims{
		Subject:   subject,
		TenantID:  tenantID,
		Email:     clientID,
		Machine:   true,
		Scopes:    scopes,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiry.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, nil
}

// issuePair creates a session and its signed access token.
func (s *Service) issuePair(ctx context.Context, row userRow,
	ip, userAgent string) (TokenPair, error) {
	refreshToken, tokenHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	session, err := createSession(ctx, s.pool, row.ID, tokenHash,
		ip, userAgent, s.tune.RefreshTTL)
	if err != nil {
		return TokenPair{}, err
	}

	now := time.Now()
	accessToken, err := s.signer.issue(Claims{
		Subject:       row.ID,
		TenantID:      row.TenantID,
		Email:         row.Email,
		PlatformAdmin: row.PlatformAdmin,
		SessionID:     session.ID,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(s.tune.AccessTTL).Unix(),
	})
	if err != nil {
		return TokenPair{}, err
	}

	user := row.User
	if err := attachTenantRoles(ctx, s.pool, &user); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.tune.AccessTTL.Seconds()),
		User:         user,
	}, nil
}
