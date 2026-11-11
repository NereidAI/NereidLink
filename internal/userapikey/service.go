// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements user API-key lifecycle and stateless request
// authentication. Credential material is stored only as an argon2id hash;
// every request repeats the database lookup so revocation takes effect
// immediately without a positive cache.
package userapikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
)

const (
	lockoutThreshold = 10
	lockoutWindow    = 15 * time.Minute
	failMapCap       = 10000
)

// Auditor appends tamper-evident events; nil is tolerated in tests.
type Auditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// PermissionReader returns a user's current permission points for a tenant.
// *rbac.Resolver satisfies it.
type PermissionReader interface {
	PermissionPointsForUser(ctx context.Context, userID, tenantID string) ([]string, error)
}

// Service manages user-scoped API keys.
type Service struct {
	pool        *pgxpool.Pool
	auditor     Auditor
	permissions PermissionReader

	failMu sync.Mutex
	fails  map[string]failState

	lastMu           sync.Mutex
	lastUsed         map[string]time.Time
	lastUsedThrottle time.Duration
}

type failState struct {
	count   int
	firstAt time.Time
}

// New constructs the service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:             pool,
		fails:            map[string]failState{},
		lastUsed:         map[string]time.Time{},
		lastUsedThrottle: 5 * time.Minute,
	}
}

// SetAuditor wires audit logging.
func (s *Service) SetAuditor(auditor Auditor) {
	s.auditor = auditor
}

// SetPermissionReader wires live owner-permission validation.
func (s *Service) SetPermissionReader(reader PermissionReader) {
	s.permissions = reader
}

// Create stores a new key and returns its plaintext exactly once.
func (s *Service) Create(ctx context.Context, ownerID string, input CreateInput) (CreateResult, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateResult{}, invalid("name is required")
	}
	if len(name) > 200 {
		return CreateResult{}, invalid("name must be at most 200 characters")
	}
	scopes, err := NormalizeScopes(input.Scopes)
	if err != nil {
		return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now()) {
		return CreateResult{}, invalid("expires_at must be in the future")
	}

	owner, err := s.loadOwner(ctx, ownerID)
	if err != nil {
		return CreateResult{}, err
	}
	if err := s.ensureScopeSubset(ctx, ownerID, owner.tenantID, scopes); err != nil {
		return CreateResult{}, err
	}

	plaintext, err := GenerateKey()
	if err != nil {
		return CreateResult{}, err
	}
	prefix, err := LookupPrefix(plaintext)
	if err != nil {
		return CreateResult{}, err
	}
	hash, err := identity.HashSecret(plaintext)
	if err != nil {
		return CreateResult{}, err
	}

	var key APIKey
	var createdAt time.Time
	err = s.pool.QueryRow(ctx, `
		INSERT INTO api_key
		    (tenant_id, user_id, name, key_prefix, key_hash, scopes,
		     expires_at, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $2::uuid)
		RETURNING id::text, tenant_id::text, user_id::text, name, key_prefix,
		          scopes, expires_at, revoked,
		          COALESCE(last_used_at::text, ''), created_at, created_by::text`,
		owner.tenantID, ownerID, name, prefix, hash, scopes, input.ExpiresAt).
		Scan(&key.ID, &key.TenantID, &key.UserID, &key.Name, &key.KeyPrefix,
			&key.Scopes, &key.ExpiresAt, &key.Revoked, &key.LastUsedAt,
			&createdAt, &key.CreatedBy)
	if err != nil {
		return CreateResult{}, mapWriteError(err)
	}
	key.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	if key.Scopes == nil {
		key.Scopes = []string{}
	}

	s.record(ctx, audit.Input{
		TenantID: owner.tenantID, ActorID: ownerID,
		EventType: audit.EventPermission, Action: "create-api-key",
		ResourceType: "api_key", ResourceID: key.ID,
		Details: map[string]any{"key_prefix": prefix, "scopes": scopes},
	})
	return CreateResult{Key: key, Plaintext: plaintext}, nil
}

// ListSelf returns the caller's own keys.
func (s *Service) ListSelf(ctx context.Context, userID string) ([]APIKey, error) {
	return s.list(ctx, "WHERE user_id = $1::uuid ORDER BY created_at", userID)
}

// ListTenant returns every key in a tenant for administrators.
func (s *Service) ListTenant(ctx context.Context, tenantID string) ([]APIKey, error) {
	return s.list(ctx, "WHERE tenant_id = $1::uuid ORDER BY created_at", tenantID)
}

// GetSelf returns one key owned by the caller.
func (s *Service) GetSelf(ctx context.Context, userID, keyID string) (APIKey, error) {
	return s.getOne(ctx, " AND user_id = $2::uuid", keyID, userID)
}

// GetTenant returns one tenant-scoped key for administrators.
func (s *Service) GetTenant(ctx context.Context, tenantID, keyID string) (APIKey, error) {
	return s.getOne(ctx, " AND tenant_id = $2::uuid", keyID, tenantID)
}

// RevokeSelf soft-revokes the caller's key while retaining audit history.
func (s *Service) RevokeSelf(ctx context.Context, userID, keyID string) (APIKey, error) {
	return s.revoke(ctx, "user_id = $2::uuid", keyID, userID, userID)
}

// RevokeTenant soft-revokes a key in a tenant.
func (s *Service) RevokeTenant(ctx context.Context, tenantID, keyID, actorID string) (APIKey, error) {
	return s.revoke(ctx, "tenant_id = $2::uuid", keyID, tenantID, actorID)
}

// DeleteSelf hard-deletes the caller's key. Audit events retain its prefix.
func (s *Service) DeleteSelf(ctx context.Context, userID, keyID string) error {
	return s.delete(ctx, "user_id = $2::uuid", keyID, userID, userID)
}

// DeleteTenant hard-deletes a key in a tenant.
func (s *Service) DeleteTenant(ctx context.Context, tenantID, keyID, actorID string) error {
	return s.delete(ctx, "tenant_id = $2::uuid", keyID, tenantID, actorID)
}

// RotateSelf replaces a key's credential and returns the new plaintext once.
func (s *Service) RotateSelf(ctx context.Context, userID, keyID string,
	newExpiry *time.Time) (RotateResult, error) {
	return s.rotate(ctx, "user_id = $2::uuid", keyID, userID, userID, newExpiry)
}

// Authenticate validates an X-Api-Key credential and returns its owner.
func (s *Service) Authenticate(ctx context.Context, plaintext, peerIP string) (AuthResult, error) {
	prefix, err := LookupPrefix(plaintext)
	if err != nil {
		s.record(ctx, audit.Input{
			ActorName: "unknown", EventType: audit.EventAuthLoginFailed,
			Action: "api-key", IP: peerIP, ResourceType: "api_key",
			Details: map[string]any{"reason": "malformed-key"},
		})
		return AuthResult{}, ErrInvalidCredentials
	}
	if s.isLocked(prefix) {
		return AuthResult{}, ErrLocked
	}

	var keyID, tenantID, userID, hash, lastUsed string
	var scopes []string
	var expiresAt *time.Time
	var revoked, userDisabled bool
	var tenantStatus string
	err = s.pool.QueryRow(ctx, `
		SELECT k.id::text, k.tenant_id::text, k.user_id::text, k.key_hash,
		       k.scopes, k.expires_at, k.revoked,
		       COALESCE(k.last_used_at::text, ''), u.disabled, t.status
		FROM api_key k
		JOIN app_user u ON u.id = k.user_id
		JOIN tenant t ON t.id = k.tenant_id
		WHERE k.key_prefix = $1`, prefix).
		Scan(&keyID, &tenantID, &userID, &hash, &scopes, &expiresAt,
			&revoked, &lastUsed, &userDisabled, &tenantStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, "", peerIP, "unknown-key")
		return AuthResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return AuthResult{}, err
	}

	ok, err := identity.VerifySecret(plaintext, hash)
	if err != nil || !ok {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, keyID, peerIP, "bad-secret")
		return AuthResult{}, ErrInvalidCredentials
	}
	if revoked {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, keyID, peerIP, "revoked-key")
		return AuthResult{}, ErrInvalidCredentials
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, keyID, peerIP, "expired-key")
		return AuthResult{}, ErrInvalidCredentials
	}
	if userDisabled {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, keyID, peerIP, "user-disabled")
		return AuthResult{}, ErrUserDisabled
	}
	if tenantStatus != "ACTIVE" {
		s.registerFailure(prefix)
		s.auditFailure(ctx, prefix, keyID, peerIP, "tenant-inactive")
		return AuthResult{}, ErrTenantInactive
	}
	if len(scopes) > 0 {
		if err := s.ensureScopeSubset(ctx, userID, tenantID, scopes); err != nil {
			s.registerFailure(prefix)
			s.auditFailure(ctx, prefix, keyID, peerIP, "scope-no-longer-held")
			return AuthResult{}, err
		}
	}

	s.clearFailures(prefix)
	s.touchLastUsed(ctx, keyID, lastUsed)
	// Successful API-key verification happens on every request, so it is
	// represented by throttled last_used_at rather than an append-only audit
	// event per call. Failed attempts and lifecycle changes remain audited.
	return AuthResult{
		KeyID: keyID, TenantID: tenantID, UserID: userID,
		Scopes: orEmpty(scopes),
	}, nil
}

type ownerState struct {
	tenantID string
	disabled bool
	status   string
}

func (s *Service) loadOwner(ctx context.Context, userID string) (ownerState, error) {
	var owner ownerState
	err := s.pool.QueryRow(ctx, `
		SELECT u.tenant_id::text, u.disabled, t.status
		FROM app_user u
		JOIN tenant t ON t.id = u.tenant_id
		WHERE u.id = $1::uuid`, userID).
		Scan(&owner.tenantID, &owner.disabled, &owner.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownerState{}, ErrNotFound
	}
	if err != nil {
		return ownerState{}, err
	}
	if owner.disabled {
		return ownerState{}, ErrUserDisabled
	}
	if owner.status != "ACTIVE" {
		return ownerState{}, ErrTenantInactive
	}
	return owner, nil
}

func (s *Service) ensureScopeSubset(ctx context.Context, userID, tenantID string, scopes []string) error {
	if len(scopes) == 0 {
		return nil
	}
	if s.permissions == nil {
		return fmt.Errorf("user API-key permission reader is not wired")
	}
	held, err := s.permissions.PermissionPointsForUser(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, point := range held {
		allowed[point] = true
	}
	// A tenant admin's role stores the wildcard rather than enumerating
	// every point; semantically it currently holds the complete catalog.
	if allowed[rbac.PointAll] {
		return nil
	}
	for _, scope := range scopes {
		if !allowed[scope] {
			return fmt.Errorf("%w: %s", ErrScopeNotAllowed, scope)
		}
	}
	return nil
}

const keySelect = `
	SELECT id::text, tenant_id::text, user_id::text, name, key_prefix,
	       scopes, expires_at, revoked, COALESCE(last_used_at::text, ''),
	       created_at, created_by::text
	FROM api_key`

func (s *Service) list(ctx context.Context, predicate string, arg string) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, keySelect+" "+predicate, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeys(rows)
}

func (s *Service) getOne(ctx context.Context, predicate string, keyID, arg string) (APIKey, error) {
	rows, err := s.pool.Query(ctx,
		keySelect+" WHERE id = $1::uuid"+predicate, keyID, arg)
	if err != nil {
		return APIKey{}, err
	}
	defer rows.Close()
	keys, err := scanKeys(rows)
	if err != nil {
		return APIKey{}, err
	}
	if len(keys) == 0 {
		return APIKey{}, ErrNotFound
	}
	return keys[0], nil
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanKeys(rows rowScanner) ([]APIKey, error) {
	out := []APIKey{}
	for rows.Next() {
		var key APIKey
		var createdAt time.Time
		if err := rows.Scan(&key.ID, &key.TenantID, &key.UserID, &key.Name,
			&key.KeyPrefix, &key.Scopes, &key.ExpiresAt, &key.Revoked,
			&key.LastUsedAt, &createdAt, &key.CreatedBy); err != nil {
			return nil, err
		}
		key.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		key.Scopes = orEmpty(key.Scopes)
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Service) revoke(ctx context.Context, scopePredicate string,
	keyID, scopeArg, actorID string) (APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIKey{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID, ownerID, prefix string
	err = tx.QueryRow(ctx,
		"SELECT tenant_id::text, user_id::text, key_prefix FROM api_key WHERE id = $1::uuid AND "+scopePredicate+" FOR UPDATE",
		keyID, scopeArg).Scan(&tenantID, &ownerID, &prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE api_key SET revoked = TRUE WHERE id = $1::uuid`, keyID); err != nil {
		return APIKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return APIKey{}, err
	}
	s.record(ctx, audit.Input{
		TenantID: tenantID, ActorID: actorID, EventType: audit.EventPermission,
		Action: "revoke-api-key", ResourceType: "api_key", ResourceID: keyID,
		Details: map[string]any{"key_prefix": prefix},
	})
	return s.getOne(ctx, " AND id = $1::uuid", keyID, keyID)
}

func (s *Service) delete(ctx context.Context, scopePredicate string,
	keyID, scopeArg, actorID string) error {
	var tenantID, ownerID, prefix string
	err := s.pool.QueryRow(ctx,
		"SELECT tenant_id::text, user_id::text, key_prefix FROM api_key WHERE id = $1::uuid AND "+scopePredicate,
		keyID, scopeArg).Scan(&tenantID, &ownerID, &prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM api_key WHERE id = $1::uuid", keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.record(ctx, audit.Input{
		TenantID: tenantID, ActorID: actorID, EventType: audit.EventPermission,
		Action: "delete-api-key", ResourceType: "api_key", ResourceID: keyID,
		Details: map[string]any{"key_prefix": prefix},
	})
	return nil
}

func (s *Service) rotate(ctx context.Context, scopePredicate string,
	keyID, scopeArg, actorID string, newExpiry *time.Time) (RotateResult, error) {
	if newExpiry != nil && newExpiry.Before(time.Now()) {
		return RotateResult{}, invalid("expires_at must be in the future")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RotateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID, ownerID, oldPrefix string
	var revoked bool
	var expiresAt *time.Time
	err = tx.QueryRow(ctx,
		"SELECT tenant_id::text, user_id::text, key_prefix, revoked, expires_at FROM api_key WHERE id = $1::uuid AND "+scopePredicate+" FOR UPDATE",
		keyID, scopeArg).Scan(&tenantID, &ownerID, &oldPrefix, &revoked, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RotateResult{}, ErrNotFound
	}
	if err != nil {
		return RotateResult{}, err
	}
	if revoked {
		return RotateResult{}, invalid("cannot rotate a revoked API key")
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return RotateResult{}, invalid("cannot rotate an expired API key")
	}
	plaintext, err := GenerateKey()
	if err != nil {
		return RotateResult{}, err
	}
	newPrefix, err := LookupPrefix(plaintext)
	if err != nil {
		return RotateResult{}, err
	}
	hash, err := identity.HashSecret(plaintext)
	if err != nil {
		return RotateResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE api_key
		SET key_prefix = $3, key_hash = $4,
		    expires_at = COALESCE($5, expires_at)
		WHERE id = $1::uuid`,
		keyID, scopeArg, newPrefix, hash, newExpiry); err != nil {
		return RotateResult{}, mapWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateResult{}, err
	}
	key, err := s.getOne(ctx, " AND id = $1::uuid", keyID, keyID)
	if err != nil {
		return RotateResult{}, err
	}
	s.record(ctx, audit.Input{
		TenantID: tenantID, ActorID: actorID, EventType: audit.EventPermission,
		Action: "rotate-api-key", ResourceType: "api_key", ResourceID: keyID,
		Details: map[string]any{"old_prefix": oldPrefix, "key_prefix": newPrefix},
	})
	return RotateResult{Key: key, Plaintext: plaintext, OldPrefix: oldPrefix}, nil
}

func (s *Service) touchLastUsed(ctx context.Context, keyID, lastUsed string) {
	now := time.Now()
	s.lastMu.Lock()
	if previous, ok := s.lastUsed[keyID]; ok && now.Sub(previous) < s.lastUsedThrottle {
		s.lastMu.Unlock()
		return
	}
	s.lastUsed[keyID] = now
	s.lastMu.Unlock()

	if _, err := s.pool.Exec(ctx,
		`UPDATE api_key SET last_used_at = now() WHERE id = $1::uuid`, keyID); err != nil {
		slog.Warn("update API key last_used_at", "error", err)
	}
}

func (s *Service) isLocked(prefix string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	state, ok := s.fails[prefix]
	if !ok {
		return false
	}
	if time.Since(state.firstAt) > lockoutWindow {
		delete(s.fails, prefix)
		return false
	}
	return state.count >= lockoutThreshold
}

func (s *Service) registerFailure(prefix string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if len(s.fails) >= failMapCap {
		for key, state := range s.fails {
			if time.Since(state.firstAt) > lockoutWindow {
				delete(s.fails, key)
			}
		}
		if len(s.fails) >= failMapCap {
			return
		}
	}
	state := s.fails[prefix]
	if state.count == 0 || time.Since(state.firstAt) > lockoutWindow {
		state = failState{firstAt: time.Now()}
	}
	state.count++
	s.fails[prefix] = state
}

func (s *Service) clearFailures(prefix string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	delete(s.fails, prefix)
}

func (s *Service) record(ctx context.Context, input audit.Input) {
	if s.auditor == nil {
		return
	}
	if _, err := s.auditor.Append(ctx, input); err != nil {
		slog.Warn("write API key audit event", "error", err)
	}
}

func (s *Service) auditFailure(ctx context.Context, prefix, keyID, peerIP, reason string) {
	s.record(ctx, audit.Input{
		ActorName: prefix, EventType: audit.EventAuthLoginFailed,
		Action: "api-key", IP: peerIP, ResourceType: "api_key",
		ResourceID: keyID,
		Details:    map[string]any{"reason": reason, "key_prefix": prefix},
	})
}

func orEmpty(scopes []string) []string {
	if scopes == nil {
		return []string{}
	}
	return scopes
}

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: key prefix already exists", ErrConflict)
	}
	return err
}
