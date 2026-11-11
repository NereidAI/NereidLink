// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements service-account lifecycle and the
// client_credentials grant. All state changes are transactional and all
// secret comparisons go through the shared argon2id verifier; the
// plaintext secret exists only in the create/rotation response.

package machineauth

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
	"github.com/nereidai/nereidlink/internal/secrettoken"
)

// clientIDPrefix marks machine-account identifiers in logs and audit
// records.
const clientIDPrefix = "sa_"

// clientIDEntropyBytes yields a 26-character unpadded base32 suffix.
const clientIDEntropyBytes = 16

// secretEntropyBytes yields a 52-character unpadded base32 secret.
const secretEntropyBytes = 32

// lockoutThreshold / lockoutWindow implement the token-endpoint brute
// force guard from ADR-0021 ("temporary lockout after failures"): more than this many failed
// grants for one client inside the window lock further attempts until the
// window passes. The counter is keyed on client id, not peer address:
// RemoteAddr carries an ephemeral client port and X-Forwarded-For can be
// spoofed before a trusted proxy, so an IP-based key let every retry look
// like a new peer and never locked. An attacker spraying random unknown
// client ids gains nothing (those ids cannot authenticate), while brute
// forcing one real client is bounded. The peer address is still recorded
// in the audit event.
const (
	lockoutThreshold = 10
	lockoutWindow    = 15 * time.Minute
)

// TokenIssuer signs the short-lived machine access token.
// identity.Service satisfies it.
type TokenIssuer interface {
	IssueMachineAccessToken(subject, tenantID, clientID string,
		scopes []string) (string, time.Time, error)
}

// Auditor appends tamper-evident events; nil is tolerated in tests.
type Auditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// Service manages service accounts and the client_credentials grant.
type Service struct {
	pool    *pgxpool.Pool
	issuer  TokenIssuer
	auditor Auditor

	// failMu guards the in-process credential lockout.
	failMu sync.Mutex
	fails  map[string]failState
}

// failState is one client+peer failure window.
type failState struct {
	count   int
	firstAt time.Time
}

// New constructs the service.
func New(pool *pgxpool.Pool, issuer TokenIssuer) *Service {
	return &Service{
		pool:   pool,
		issuer: issuer,
		fails:  map[string]failState{},
	}
}

// SetAuditor wires the audit log.
func (s *Service) SetAuditor(a Auditor) {
	s.auditor = a
}

// generateID returns a new public client identifier.
func generateID() (string, error) {
	return randomToken(clientIDPrefix, clientIDEntropyBytes)
}

// generateSecret returns a new plaintext client secret.
func generateSecret() (string, error) {
	return randomToken("", secretEntropyBytes)
}

// randomToken returns prefix plus an unpadded base32 encoding of random
// bytes.
func randomToken(prefix string, n int) (string, error) {
	return secrettoken.Random(prefix, n)
}

// Create provisions an enabled service account and its PRIMARY secret.
// The plaintext secret is returned in the result and never stored.
func (s *Service) Create(ctx context.Context, tenantID string,
	input CreateInput) (CreateResult, error) {
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

	clientID, err := generateID()
	if err != nil {
		return CreateResult{}, err
	}
	secret, err := generateSecret()
	if err != nil {
		return CreateResult{}, err
	}
	secretHash, err := identity.HashSecret(secret)
	if err != nil {
		return CreateResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id, createdAt, updatedAt string
	err = tx.QueryRow(ctx, `
		INSERT INTO service_account
		    (tenant_id, client_id, name, scopes, enabled)
		VALUES ($1::uuid, $2, $3, $4, TRUE)
		RETURNING id::text, created_at::text, updated_at::text`,
		tenantID, clientID, name, scopes).
		Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		return CreateResult{}, mapWriteError(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO service_account_secret
		    (service_account_id, secret_hash, label, expires_at)
		VALUES ($1::uuid, $2, $3, $4)`,
		id, secretHash, LabelPrimary, input.SecretExpiresAt); err != nil {
		return CreateResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}

	account := ServiceAccount{
		ID: id, TenantID: tenantID, ClientID: clientID, Name: name,
		Scopes: scopes, Enabled: true,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "create-service-account",
		ResourceType: "service_account", ResourceID: id,
		Details: map[string]any{"client_id": clientID, "scopes": scopes},
	})
	return CreateResult{
		Account:     account,
		Credentials: Credentials{ClientID: clientID, ClientSecret: secret},
	}, nil
}

// RotateSecret performs overlap rotation: the old PRIMARY is demoted to
// ROTATING (replacing any previously rotating secret) and a fresh secret
// becomes PRIMARY. The new plaintext is returned once.
func (s *Service) RotateSecret(ctx context.Context, tenantID, accountID string,
	newExpiry *time.Time) (RotateResult, error) {
	secret, err := generateSecret()
	if err != nil {
		return RotateResult{}, err
	}
	secretHash, err := identity.HashSecret(secret)
	if err != nil {
		return RotateResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RotateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the account row so two concurrent rotations cannot both
	// demote the same PRIMARY.
	var clientID string
	err = tx.QueryRow(ctx, `
		SELECT client_id
		FROM service_account
		WHERE id = $1::uuid AND tenant_id = $2::uuid
		FOR UPDATE`, accountID, tenantID).
		Scan(&clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RotateResult{}, ErrNotFound
		}
		return RotateResult{}, err
	}

	// Drop the oldest secret, then demote the current PRIMARY. Deleting
	// first keeps the table at most two rows per account.
	var rotatingExpires *time.Time
	if err := tx.QueryRow(ctx, `
		DELETE FROM service_account_secret
		WHERE service_account_id = $1::uuid AND label = $2
		RETURNING expires_at`, accountID, LabelRotating).
		Scan(&rotatingExpires); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RotateResult{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE service_account_secret SET label = $3
		WHERE service_account_id = $1::uuid AND label = $2`,
		accountID, LabelPrimary, LabelRotating); err != nil {
		return RotateResult{}, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO service_account_secret
		    (service_account_id, secret_hash, label, expires_at)
		VALUES ($1::uuid, $2, $3, $4)`,
		accountID, secretHash, LabelPrimary, newExpiry); err != nil {
		return RotateResult{}, err
	}

	// Rotation changes the credentials of the account, so bump its
	// updated_at even though no account column changed.
	if _, err := tx.Exec(ctx, `
		UPDATE service_account SET updated_at = now()
		WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		accountID, tenantID); err != nil {
		return RotateResult{}, err
	}

	account, err := readAccount(ctx, tx, accountID, tenantID)
	if err != nil {
		return RotateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateResult{}, err
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "rotate-service-account-secret",
		ResourceType: "service_account", ResourceID: accountID,
		Details: map[string]any{"client_id": clientID},
	})

	result := RotateResult{
		Account:   account,
		ClientID:  clientID,
		NewSecret: secret,
	}
	if rotatingExpires != nil {
		result.RotatingExpiresAt = rotatingExpires.UTC().Format(time.RFC3339)
	}
	return result, nil
}

// List returns every service account of a tenant.
func (s *Service) List(ctx context.Context, tenantID string) ([]ServiceAccount, error) {
	rows, err := s.pool.Query(ctx, accountSelect+`
		WHERE tenant_id = $1::uuid
		ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAccounts(rows)
}

// Get returns one tenant-scoped service account.
func (s *Service) Get(ctx context.Context, tenantID, accountID string) (ServiceAccount, error) {
	return s.getOne(ctx, ` AND id = $2::uuid`, tenantID, accountID)
}

// SetEnabled switches an account without deleting it, providing instant
// lockout of a leaked credential.
func (s *Service) SetEnabled(ctx context.Context, tenantID, accountID string,
	enabled bool) (ServiceAccount, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID string
	err = tx.QueryRow(ctx, `
		SELECT client_id
		FROM service_account
		WHERE id = $1::uuid AND tenant_id = $2::uuid
		FOR UPDATE`, accountID, tenantID).Scan(&clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceAccount{}, ErrNotFound
		}
		return ServiceAccount{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE service_account SET enabled = $3, updated_at = now()
		WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		accountID, tenantID, enabled); err != nil {
		return ServiceAccount{}, err
	}

	account, err := readAccount(ctx, tx, accountID, tenantID)
	if err != nil {
		return ServiceAccount{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceAccount{}, err
	}
	action := "enable-service-account"
	if !enabled {
		action = "disable-service-account"
	}
	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       action,
		ResourceType: "service_account", ResourceID: account.ID,
		Details: map[string]any{"client_id": clientID},
	})
	return account, nil
}

// Update changes the operator-facing fields of an account. Nil arguments
// leave the field untouched; a non-nil empty name is rejected. Scopes are
// re-validated against the RBAC point catalog.
func (s *Service) Update(ctx context.Context, tenantID, accountID string,
	name *string, scopes []string, setScopes bool) (ServiceAccount, error) {
	var newName *string
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" || len(trimmed) > 200 {
			return ServiceAccount{}, invalid("name must be 1..200 characters")
		}
		newName = &trimmed
	}
	var normalized []string
	if setScopes {
		var err error
		normalized, err = NormalizeScopes(scopes)
		if err != nil {
			return ServiceAccount{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID string
	err = tx.QueryRow(ctx, `
		SELECT client_id FROM service_account
		WHERE id = $1::uuid AND tenant_id = $2::uuid
		FOR UPDATE`, accountID, tenantID).Scan(&clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceAccount{}, ErrNotFound
		}
		return ServiceAccount{}, err
	}

	if newName != nil || setScopes {
		if _, err := tx.Exec(ctx, `
			UPDATE service_account
			SET name = COALESCE($3, name),
			    scopes = CASE WHEN $4 THEN $5 ELSE scopes END,
			    updated_at = now()
			WHERE id = $1::uuid AND tenant_id = $2::uuid`,
			accountID, tenantID, newName, setScopes, normalized); err != nil {
			return ServiceAccount{}, mapWriteError(err)
		}
	}

	account, err := readAccount(ctx, tx, accountID, tenantID)
	if err != nil {
		return ServiceAccount{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceAccount{}, err
	}
	s.record(ctx, audit.Input{
		TenantID: tenantID, EventType: audit.EventPermission,
		Action:       "update-service-account",
		ResourceType: "service_account", ResourceID: accountID,
		Details: map[string]any{"client_id": clientID},
	})
	return account, nil
}

// Delete removes an account and its secrets via the cascade.
func (s *Service) Delete(ctx context.Context, tenantID, accountID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM service_account WHERE id = $1::uuid AND tenant_id = $2::uuid`,
		accountID, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.record(ctx, audit.Input{
		TenantID: tenantID, EventType: audit.EventPermission,
		Action:       "delete-service-account",
		ResourceType: "service_account", ResourceID: accountID,
	})
	return nil
}

// TokenResult is a successful client_credentials grant.
type TokenResult struct {
	// AccessToken is the bearer token.
	AccessToken string
	// ExpiresAt is its absolute expiry.
	ExpiresAt time.Time
}

// IssueToken authenticates client credentials and issues a machine
// access token scoped to the account. Unknown client and bad secret
// return the same error to prevent enumeration.
func (s *Service) IssueToken(ctx context.Context,
	clientID, clientSecret, peerIP string) (TokenResult, error) {
	if s.isLocked(clientID) {
		return TokenResult{}, ErrLocked
	}

	account, secrets, err := s.loadForAuth(ctx, clientID)
	if err != nil {
		s.registerFailure(clientID)
		s.auditAuthFailure(ctx, clientID, peerIP, "unknown-client")
		return TokenResult{}, ErrInvalidCredentials
	}
	if !account.Enabled {
		// Disabled is a distinct state but still a failure for the
		// lockout counter so a disabled account cannot be probed freely.
		s.registerFailure(clientID)
		return TokenResult{}, ErrDisabled
	}

	matched := false
	var matchedLabel string
	for _, candidate := range secrets {
		if candidate.expiresAt != nil && candidate.expiresAt.Before(time.Now()) {
			continue
		}
		ok, err := identity.VerifySecret(clientSecret, candidate.hash)
		if err != nil {
			continue
		}
		if ok {
			matched = true
			matchedLabel = candidate.label
			break
		}
	}
	if !matched {
		s.registerFailure(clientID)
		s.auditAuthFailure(ctx, clientID, peerIP, "bad-secret")
		return TokenResult{}, ErrInvalidCredentials
	}

	token, expiry, err := s.issuer.IssueMachineAccessToken(
		account.ID, account.TenantID, account.ClientID, account.Scopes)
	if err != nil {
		return TokenResult{}, err
	}

	s.clearFailures(clientID)
	// last_used_at is observability data, updated synchronously like the
	// audit event: it is one indexed update and keeping it on the request
	// context means a cancelled grant never leaves a detached goroutine.
	if _, err := s.pool.Exec(ctx,
		`UPDATE service_account SET last_used_at = now() WHERE id = $1::uuid`,
		account.ID); err != nil {
		slog.Warn("update service account last_used_at", "error", err)
	}

	s.auditAuthSuccess(ctx, account, peerIP, matchedLabel)
	return TokenResult{AccessToken: token, ExpiresAt: expiry}, nil
}

// storedSecret is one secret row loaded for authentication.
type storedSecret struct {
	label     string
	hash      string
	expiresAt *time.Time
}

// loadForAuth loads an enabled-or-not account with both secret rows.
func (s *Service) loadForAuth(ctx context.Context,
	clientID string) (ServiceAccount, []storedSecret, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ServiceAccount{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var account ServiceAccount
	var scopes []string
	err = tx.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, client_id, name, scopes, enabled,
		       created_at::text, updated_at::text
		FROM service_account WHERE client_id = $1`, clientID).
		Scan(&account.ID, &account.TenantID, &account.ClientID, &account.Name,
			&scopes, &account.Enabled, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceAccount{}, nil, ErrNotFound
		}
		return ServiceAccount{}, nil, err
	}
	account.Scopes = scopes

	rows, err := tx.Query(ctx, `
		SELECT label, secret_hash, expires_at
		FROM service_account_secret
		WHERE service_account_id = $1::uuid`, account.ID)
	if err != nil {
		return ServiceAccount{}, nil, err
	}
	defer rows.Close()
	var secrets []storedSecret
	for rows.Next() {
		var ss storedSecret
		if err := rows.Scan(&ss.label, &ss.hash, &ss.expiresAt); err != nil {
			return ServiceAccount{}, nil, err
		}
		secrets = append(secrets, ss)
	}
	return account, secrets, rows.Err()
}

// isLocked reports whether the client is inside an active lockout.
func (s *Service) isLocked(clientID string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	state, ok := s.fails[clientID]
	if !ok {
		return false
	}
	if time.Since(state.firstAt) > lockoutWindow {
		delete(s.fails, clientID)
		return false
	}
	return state.count >= lockoutThreshold
}

// registerFailure adds one failure and opens a new window if needed.
func (s *Service) registerFailure(clientID string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	// Bound the map: failed attempts against random, never-retried
	// client ids would otherwise grow it without limit (an attacker
	// controls the client id). Once it crosses the cap, drop every
	// expired window and refuse to keep adding beyond the hard cap.
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
	state := s.fails[clientID]
	if state.count == 0 || time.Since(state.firstAt) > lockoutWindow {
		state = failState{firstAt: time.Now()}
	}
	state.count++
	s.fails[clientID] = state
}

// failMapCap bounds the in-process failure map; well above any realistic
// number of distinct real clients and far below memory pressure.
const failMapCap = 10000

// clearFailures removes the window after a successful grant.
func (s *Service) clearFailures(clientID string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	delete(s.fails, clientID)
}

// accountSelect is the shared column list for management reads.
const accountSelect = `
	SELECT id::text, tenant_id::text, client_id, name, scopes, enabled,
	       COALESCE(last_used_at::text, ''), created_at::text, updated_at::text
	FROM service_account`

// rowScanner abstracts pgx.Rows for the shared scan helper.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanAccounts(rows rowScanner) ([]ServiceAccount, error) {
	var out []ServiceAccount
	for rows.Next() {
		var a ServiceAccount
		var scopes []string
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ClientID, &a.Name,
			&scopes, &a.Enabled, &a.LastUsedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.Scopes = orEmpty(scopes)
		out = append(out, a)
	}
	return out, rows.Err()
}

// getOne runs the shared select with an extra predicate.
func (s *Service) getOne(ctx context.Context, predicate string,
	tenantID string, arg string) (ServiceAccount, error) {
	rows, err := s.pool.Query(ctx,
		accountSelect+" WHERE tenant_id = $1::uuid"+predicate, tenantID, arg)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer rows.Close()
	accounts, err := scanAccounts(rows)
	if err != nil {
		return ServiceAccount{}, err
	}
	if len(accounts) == 0 {
		return ServiceAccount{}, ErrNotFound
	}
	return accounts[0], nil
}

// readAccount loads one account inside an open transaction.
func readAccount(ctx context.Context, tx pgx.Tx, accountID, tenantID string) (ServiceAccount, error) {
	rows, err := tx.Query(ctx,
		accountSelect+" WHERE id = $1::uuid AND tenant_id = $2::uuid",
		accountID, tenantID)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer rows.Close()
	accounts, err := scanAccounts(rows)
	if err != nil {
		return ServiceAccount{}, err
	}
	if len(accounts) == 0 {
		return ServiceAccount{}, ErrNotFound
	}
	return accounts[0], nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
}

// mapWriteError turns unique violations into ErrConflict.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: client_id or name already exists", ErrConflict)
	}
	return err
}

// record appends an audit event when auditing is wired.
func (s *Service) record(ctx context.Context, input audit.Input) {
	if s.auditor == nil {
		return
	}
	if _, err := s.auditor.Append(ctx, input); err != nil {
		slog.Warn("write service account audit event", "error", err)
	}
}

// auditAuthFailure records a rejected grant.
func (s *Service) auditAuthFailure(ctx context.Context, clientID, peerIP, reason string) {
	s.record(ctx, audit.Input{
		ActorName: clientID, EventType: audit.EventAuthLoginFailed,
		Action: "client-credentials", IP: peerIP,
		ResourceType: "service_account",
		Details:      map[string]any{"reason": reason},
	})
}

// auditAuthSuccess records an accepted grant.
func (s *Service) auditAuthSuccess(ctx context.Context, a ServiceAccount,
	peerIP, label string) {
	s.record(ctx, audit.Input{
		TenantID: a.TenantID, ActorID: a.ID, ActorName: a.ClientID,
		EventType: audit.EventAuthLogin, Action: "client-credentials",
		IP: peerIP, ResourceType: "service_account", ResourceID: a.ID,
		Details: map[string]any{"secret_label": label, "scopes": a.Scopes},
	})
}
