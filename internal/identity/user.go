// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// user.go defines the user model and the account queries used by
// login, provisioning and the future users management page.

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is one local identity account.
type User struct {
	// ID is the stable user identifier.
	ID string `json:"id"`

	// TenantID is the owning tenant; empty for platform-only admins.
	TenantID string `json:"tenant_id,omitempty"`

	// Email is the per-tenant unique login identifier.
	Email string `json:"email"`

	// DisplayName is the operator-facing name.
	DisplayName string `json:"display_name"`

	// PlatformAdmin marks a cross-tenant administrator.
	PlatformAdmin bool `json:"is_platform_admin"`

	// EmailVerified marks ownership proof.
	EmailVerified bool `json:"email_verified"`

	// MustChangePassword forces a reset at next login.
	MustChangePassword bool `json:"must_change_password"`

	// LastLoginAt is the previous login time as RFC3339 UTC.
	LastLoginAt string `json:"last_login_at,omitempty"`

	// CreatedAt is the account creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// TenantRoles lists the built-in roles held in each membership
	// tenant, keyed by tenant id. It is populated on the authenticated
	// profile (/v1/auth/me) and login responses for UI gating (for
	// example member administration, delegated to TENANT_ADMIN); it is
	// absent on accounts with no membership.
	TenantRoles map[string][]string `json:"tenant_roles,omitempty"`
}

// CreateUserInput carries the fields needed to open an account.
type CreateUserInput struct {
	// TenantID may be empty for a platform admin.
	TenantID string

	// Email is normalized to lower-case and trimmed.
	Email string

	// DisplayName defaults to the email local part when empty.
	DisplayName string

	// Password stores the plaintext to hash; it never leaves the
	// process.
	Password string

	// PlatformAdmin creates a cross-tenant admin.
	PlatformAdmin bool

	// EmailVerified records that ownership was proven out of band.
	EmailVerified bool
}

// CreateUser validates, hashes and inserts one account.
func CreateUser(ctx context.Context, pool *pgxpool.Pool,
	policy PasswordPolicy, input CreateUserInput) (User, error) {
	email := normalizeEmail(input.Email)
	if email == "" {
		return User{}, fmt.Errorf("%w: email is required", ErrInvalidArgument)
	}
	if err := ValidatePassword(input.Password, 10); err != nil {
		return User{}, err
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = strings.SplitN(email, "@", 2)[0]
	}

	hash, err := hashPassword(input.Password, policy)
	if err != nil {
		return User{}, err
	}

	id := uuid.NewString()
	var tenantID any
	if input.TenantID != "" {
		tenantID = input.TenantID
	}

	row := pool.QueryRow(ctx,
		`INSERT INTO app_user
		   (id, tenant_id, email, display_name, password_hash,
		    is_platform_admin, email_verified)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING created_at`,
		id, tenantID, email, displayName, hash,
		input.PlatformAdmin, input.EmailVerified)

	var createdAt time.Time
	if err := row.Scan(&createdAt); err != nil {
		return User{}, mapIdentitySQLError(err)
	}

	return User{
		ID:            id,
		TenantID:      input.TenantID,
		Email:         email,
		DisplayName:   displayName,
		PlatformAdmin: input.PlatformAdmin,
		EmailVerified: input.EmailVerified,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339),
	}, nil
}

// userRow is the storage projection enriched with authentication
// fields the public User DTO must not expose.
type userRow struct {
	User

	// PasswordHash is the argon2id PHC string.
	PasswordHash string

	// Disabled marks an admin-disabled account.
	Disabled bool

	// FailedLoginCount tracks the current failure window.
	FailedLoginCount int

	// LockedUntil holds the lock expiry.
	LockedUntil *time.Time

	// FirstFailedAt anchors the rolling failure window.
	FirstFailedAt *time.Time
}

// userSelectColumns is the account projection shared by the login
// and user-management lookups, kept as a constant so the queries
// never drift out of sync with the scan helper.
const userSelectColumns = `id::text, tenant_id::text, email, display_name,
		        password_hash, is_platform_admin, email_verified,
		        must_change_password, disabled, failed_login_count,
		        first_failed_at, locked_until, last_login_at, created_at`

// scanUserRow scans one account projection from a rows iterator.
// Nullable timestamp and tenant columns bind through pointers so
// platform accounts (null tenant) scan cleanly.
func scanUserRow(rows pgx.Rows) (userRow, error) {
	var row userRow
	var storedTenant *string
	var firstFailed, lockedUntil, lastLogin *time.Time
	var createdAt time.Time
	if err := rows.Scan(&row.ID, &storedTenant, &row.Email, &row.DisplayName,
		&row.PasswordHash, &row.PlatformAdmin, &row.EmailVerified,
		&row.MustChangePassword, &row.Disabled, &row.FailedLoginCount,
		&firstFailed, &lockedUntil, &lastLogin, &createdAt); err != nil {
		return userRow{}, err
	}
	if storedTenant != nil {
		row.TenantID = *storedTenant
	}
	row.FirstFailedAt = firstFailed
	row.LockedUntil = lockedUntil
	if lastLogin != nil {
		row.LastLoginAt = lastLogin.UTC().Format(time.RFC3339)
	}
	row.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return row, nil
}

// findUsersByEmailGlobal loads every account sharing an email across
// tenants. Login accepts one match; the user-management service
// enforces global email uniqueness so multiple rows only arise from
// legacy or directly inserted data.
func findUsersByEmailGlobal(ctx context.Context, pool *pgxpool.Pool,
	email string) ([]userRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+userSelectColumns+`
		 FROM app_user
		 WHERE email = $1
		 ORDER BY created_at`, email)
	if err != nil {
		return nil, fmt.Errorf("find user globally: %w", err)
	}
	defer rows.Close()

	found := []userRow{}
	for rows.Next() {
		row, err := scanUserRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		found = append(found, row)
	}
	return found, rows.Err()
}

// EmailExists reports whether an email is already registered by any
// tenant. User creation enforces global uniqueness so login stays
// unambiguous without a tenant selector.
func EmailExists(ctx context.Context, pool *pgxpool.Pool, email string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_user WHERE email = $1)`, email).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check email: %w", err)
	}
	return exists, nil
}

// findUserByID loads one account for token verification.
func findUserByID(ctx context.Context, pool *pgxpool.Pool, userID string) (userRow, error) {
	var row userRow
	var storedTenant *string
	var lockedUntil *time.Time
	var lastLogin *time.Time
	var createdAt time.Time

	err := pool.QueryRow(ctx,
		`SELECT id::text, tenant_id::text, email, display_name,
		        password_hash, is_platform_admin, email_verified,
		        must_change_password, disabled, failed_login_count,
		        first_failed_at, locked_until, last_login_at, created_at
		 FROM app_user WHERE id = $1`,
		userID).Scan(
		&row.ID, &storedTenant, &row.Email, &row.DisplayName,
		&row.PasswordHash, &row.PlatformAdmin, &row.EmailVerified,
		&row.MustChangePassword, &row.Disabled, &row.FailedLoginCount,
		&row.FirstFailedAt, &lockedUntil, &lastLogin, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return userRow{}, ErrUserNotFound
	}
	if err != nil {
		return userRow{}, fmt.Errorf("find user by id: %w", err)
	}
	if storedTenant != nil {
		row.TenantID = *storedTenant
	}
	row.LockedUntil = lockedUntil
	if lastLogin != nil {
		row.LastLoginAt = lastLogin.UTC().Format(time.RFC3339)
	}
	row.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return row, nil
}

// recordLoginFailure applies the 10-per-15-minute lockout policy. The
// window is anchored at the first failure; reaching the threshold sets
// locked_until.
func recordLoginFailure(ctx context.Context, pool *pgxpool.Pool, row userRow) error {
	windowStart := time.Now().Add(-15 * time.Minute)
	count := row.FailedLoginCount + 1

	var lockedUntil any
	if row.FirstFailedAt == nil || row.FirstFailedAt.Before(windowStart) {
		count = 1
	}
	if count >= lockoutThreshold {
		lockedUntil = time.Now().Add(lockoutDuration)
	}

	_, err := pool.Exec(ctx,
		`UPDATE app_user
		 SET failed_login_count = $1,
		     first_failed_at = CASE
		         WHEN first_failed_at IS NULL OR first_failed_at < $2
		         THEN now() ELSE first_failed_at END,
		     locked_until = $3,
		     updated_at = now()
		 WHERE id = $4`,
		count, windowStart, lockedUntil, row.ID)
	return err
}

// resetLoginFailure clears the failure window after a successful login.
func resetLoginFailure(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE app_user
		 SET failed_login_count = 0, first_failed_at = NULL, locked_until = NULL,
		     last_login_at = now(), updated_at = now()
		 WHERE id = $1`, userID)
	return err
}

// normalizeEmail canonicalizes a login identifier: trim, lower-case.
func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// Lockout policy constants from ADR-0021: 10 failures within a 15
// minute window; the lock itself lasts 15 minutes.
const (
	lockoutThreshold = 10
	lockoutDuration  = 15 * time.Minute
)

// mapIdentitySQLError maps database violations to identity errors.
func mapIdentitySQLError(err error) error {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
		return fmt.Errorf("%w: a user with this email already exists in the tenant",
			ErrInvalidArgument)
	}
	return err
}
