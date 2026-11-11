// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package usermgmt implements tenant member administration for the
// platform console (ADR-0021/0022): platform operators create and
// remove tenant users, assign built-in roles, reset credentials and
// enable/disable accounts. A member is one app_user plus exactly one
// tenant membership; user records are tenant-local, so removing the
// membership removes the account as well.
package usermgmt

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
)

var (
	// ErrNotFound means the tenant or member does not exist.
	ErrNotFound = errors.New("member or tenant not found")

	// ErrConflict means the email is already registered.
	ErrConflict = errors.New("member conflict")

	// ErrInvalidArgument means a request field is invalid.
	ErrInvalidArgument = errors.New("invalid member argument")

	// ErrProtected means a platform administrator cannot be modified by
	// member administration; only password reset is allowed.
	ErrProtected = errors.New("platform administrator account is protected")
)

// Member is one tenant user management record.
type Member struct {
	// UserID identifies the account.
	UserID string `json:"user_id"`

	// Email is the login identifier.
	Email string `json:"email"`

	// DisplayName is the operator-facing name.
	DisplayName string `json:"display_name"`

	// Roles are the built-in tenant roles of the membership.
	Roles []string `json:"roles"`

	// Disabled marks an administratively switched-off account.
	Disabled bool `json:"disabled"`

	// EmailVerified marks the admin-provisioned verification state.
	EmailVerified bool `json:"email_verified"`

	// PlatformAdmin marks a cross-tenant platform administrator. Such
	// accounts are protected: roles, enable state and removal cannot be
	// changed through member administration; only password reset.
	PlatformAdmin bool `json:"is_platform_admin"`

	// DataScope is the membership level that narrows which devices the
	// member can reach: TENANT (whole tenant), CUSTOMER (one customer
	// subtree) or GROUP (one device-group subtree). It travels with the
	// member so an operator can see the narrowing without opening a device.
	DataScope string `json:"data_scope"`

	// DataScopeID is the anchoring customer or group id for a narrowed
	// scope, and empty for TENANT.
	DataScopeID string `json:"data_scope_id,omitempty"`

	// DataScopeName is the human-readable anchor label, resolved when the
	// member is loaded so the UI need not resolve ids itself.
	DataScopeName string `json:"data_scope_name,omitempty"`

	// LastLoginAt is the previous login as RFC3339 UTC.
	LastLoginAt string `json:"last_login_at,omitempty"`

	// CreatedAt is the account creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// CreateMemberInput carries the new member fields. An empty password
// makes the service generate one, returned once in the result.
type CreateMemberInput struct {
	// Email is normalized to lower-case and must be globally unused.
	Email string

	// DisplayName defaults to the email local part.
	DisplayName string

	// Password may be empty; generated passwords appear once.
	Password string

	// Roles default to TENANT_ADMIN when empty.
	Roles []string

	// DataScope defaults to TENANT when empty. SELF is rejected: no
	// resource in the current schema is owned by a user, so it cannot be
	// honored and silently widening it would leak the whole tenant.
	DataScope string

	// DataScopeID is the anchoring customer or group id, required for
	// CUSTOMER and GROUP and forbidden for TENANT.
	DataScopeID string
}

// CreateResult is the create outcome, carrying the plaintext
// GeneratedPassword only when the caller asked the service to make one.
type CreateResult struct {
	// Member is the stored account.
	Member Member

	// GeneratedPassword is empty when the caller supplied a password.
	GeneratedPassword string
}

// Auditor appends tamper-evident events. *audit.Service satisfies it.
type Auditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// Service manages tenant members.
type Service struct {
	pool    *pgxpool.Pool
	policy  identity.PasswordPolicy
	auditor Auditor
	nc      *nats.Conn
}

// New constructs the member management service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, policy: identity.DefaultPasswordPolicy()}
}

// SetAuditor wires the audit event collaborator.
func (s *Service) SetAuditor(a Auditor) {
	s.auditor = a
}

// assignableRoles are the tenant-scoped roles an operator may grant.
// PLATFORM_ADMIN is the caller's own role and SERVICE_ACCOUNT is
// reserved for future automation identities.
var assignableRoles = map[string]bool{
	rbac.RoleTenantAdmin:  true,
	rbac.RoleEngineer:     true,
	rbac.RoleCustomerUser: true,
	rbac.RoleReadOnly:     true,
}

// CreateMember opens one tenant user account, its membership and the
// requested roles in one transactional flow. The plaintext password
// is never stored again; a generated one is returned exactly once.
func (s *Service) CreateMember(ctx context.Context, tenantID string,
	input CreateMemberInput) (CreateResult, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" {
		return CreateResult{}, invalid("email is required")
	}
	roles := normalizeRoles(input.Roles)
	for _, role := range roles {
		if !assignableRoles[role] {
			return CreateResult{}, invalid("unsupported role " + role)
		}
	}
	// The scope is validated before the tenant is touched so a bad request
	// never creates a half-provisioned account. An empty scope means
	// TENANT, which is the historical behavior.
	dataScope := strings.TrimSpace(input.DataScope)
	if dataScope == "" {
		dataScope = rbac.ScopeTenant
	}
	scopeID := strings.TrimSpace(input.DataScopeID)
	if err := rbac.ValidateScope(dataScope, scopeID); err != nil {
		return CreateResult{}, invalid(err.Error())
	}
	if err := s.requireTenant(ctx, tenantID); err != nil {
		return CreateResult{}, err
	}

	exists, err := identity.EmailExists(ctx, s.pool, email)
	if err != nil {
		return CreateResult{}, err
	}
	if exists {
		return CreateResult{}, fmt.Errorf("%w: email is already registered", ErrConflict)
	}

	password := input.Password
	generated := false
	if password == "" {
		password, err = generatePassword()
		if err != nil {
			return CreateResult{}, err
		}
		generated = true
	}

	user, err := identity.CreateUser(ctx, s.pool, s.policy, identity.CreateUserInput{
		TenantID:      tenantID,
		Email:         email,
		DisplayName:   input.DisplayName,
		Password:      password,
		EmailVerified: true,
	})
	if err != nil {
		return CreateResult{}, mapIdentityError(err)
	}

	if err := rbac.EnsureMembershipScoped(ctx, s.pool, user.ID, tenantID,
		roles[0], dataScope, scopeID); err != nil {
		return CreateResult{}, mapScopeError(err)
	}
	if len(roles) > 1 {
		for _, role := range roles[1:] {
			if err := s.attachRole(ctx, user.ID, tenantID, role); err != nil {
				return CreateResult{}, err
			}
		}
	}

	member, err := s.loadMember(ctx, tenantID, user.ID)
	if err != nil {
		return CreateResult{}, err
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "create-member",
		ResourceType: "app_user",
		ResourceID:   user.ID,
		Details: map[string]any{
			"roles": roles, "data_scope": dataScope, "data_scope_id": scopeID,
		},
	})

	s.publishEvent(ctx, EventCreated, tenantID, user.ID)

	result := CreateResult{Member: member}
	if generated {
		result.GeneratedPassword = password
	}
	return result, nil
}

// ListMembers returns every user account that is a member of tenant.
func (s *Service) ListMembers(ctx context.Context, tenantID string) ([]Member, error) {
	if err := s.requireTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT u.id::text, u.email, u.display_name,
		        u.disabled, u.email_verified, u.is_platform_admin,
		        COALESCE(u.last_login_at, to_timestamp(0)),
		        u.last_login_at IS NULL,
		        u.created_at,
		        m.scope, COALESCE(m.scope_id::text, '')
		 FROM tenant_membership m
		 JOIN app_user u ON u.id = m.user_id
		 WHERE m.tenant_id = $1
		 ORDER BY u.created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := []Member{}
	for rows.Next() {
		var member Member
		var lastLogin time.Time
		var lastLoginNull bool
		var createdAt time.Time
		if err := rows.Scan(&member.UserID, &member.Email, &member.DisplayName,
			&member.Disabled, &member.EmailVerified, &member.PlatformAdmin,
			&lastLogin, &lastLoginNull, &createdAt,
			&member.DataScope, &member.DataScopeID); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		if !lastLoginNull {
			member.LastLoginAt = lastLogin.UTC().Format(time.RFC3339)
		}
		member.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		roles, err := s.loadRoles(ctx, member.UserID, tenantID)
		if err != nil {
			return nil, err
		}
		member.Roles = roles
		if err := s.resolveScopeName(ctx, tenantID, &member); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// SetRoles replaces the role set of one member.
func (s *Service) SetRoles(ctx context.Context, tenantID, userID string, roles []string) (Member, error) {
	roles = normalizeRoles(roles)
	if len(roles) == 0 {
		return Member{}, invalid("at least one role is required")
	}
	for _, role := range roles {
		if !assignableRoles[role] {
			return Member{}, invalid("unsupported role " + role)
		}
	}
	loaded, err := s.loadMember(ctx, tenantID, userID)
	if err != nil {
		return Member{}, err
	}
	if loaded.PlatformAdmin {
		return Member{}, ErrProtected
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var membershipID string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM tenant_membership
		 WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID).Scan(&membershipID); err != nil {
		return Member{}, mapIdentityError(err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM membership_role WHERE membership_id = $1`, membershipID); err != nil {
		return Member{}, err
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx,
			`INSERT INTO membership_role (id, membership_id, role)
			 VALUES (gen_random_uuid(), $1, $2)`,
			membershipID, role); err != nil {
			return Member{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, err
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "set-roles",
		ResourceType: "app_user",
		ResourceID:   userID,
		Details:      map[string]any{"roles": roles},
	})
	s.publishEvent(ctx, EventRoles, tenantID, userID)
	return s.loadMember(ctx, tenantID, userID)
}

// ResetPassword replaces the credential of one member and clears the
// login lock state.
func (s *Service) ResetPassword(ctx context.Context, tenantID, userID, newPassword string) (string, error) {
	loaded, err := s.loadMember(ctx, tenantID, userID)
	if err != nil {
		return "", err
	}
	if loaded.PlatformAdmin {
		// Delegated member administration makes this guard load-bearing:
		// a tenant admin must never seize a platform admin account by
		// resetting its password.
		return "", ErrProtected
	}
	password := newPassword
	if password == "" {
		generated, err := generatePassword()
		if err != nil {
			return "", err
		}
		password = generated
	} else if err := identity.ValidatePassword(password, 10); err != nil {
		return "", mapIdentityError(err)
	}
	if err := identity.ResetPassword(ctx, s.pool, s.policy, userID, password); err != nil {
		return "", mapIdentityError(err)
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "reset-password",
		ResourceType: "app_user",
		ResourceID:   userID,
	})
	return password, nil
}

// SetDisabled enables or disables one member account.
func (s *Service) SetDisabled(ctx context.Context, tenantID, userID string, disabled bool) (Member, error) {
	loaded, err := s.loadMember(ctx, tenantID, userID)
	if err != nil {
		return Member{}, err
	}
	if loaded.PlatformAdmin {
		return Member{}, ErrProtected
	}
	if err := identity.SetAccountDisabled(ctx, s.pool, userID, disabled); err != nil {
		return Member{}, mapIdentityError(err)
	}

	action := "enable"
	kind := EventEnabled
	if disabled {
		action = "disable-member"
		kind = EventDisabled
	}
	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       action,
		ResourceType: "app_user",
		ResourceID:   userID,
	})
	s.publishEvent(ctx, kind, tenantID, userID)
	return s.loadMember(ctx, tenantID, userID)
}

// RemoveMember deletes the tenant membership and the tenant-local
// account; sessions and role rows cascade with the user.
func (s *Service) RemoveMember(ctx context.Context, tenantID, userID string) error {
	loaded, err := s.loadMember(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	if loaded.PlatformAdmin {
		return ErrProtected
	}
	if err := s.requireNotSelf(ctx, userID); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var membershipID string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM tenant_membership
		 WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID).Scan(&membershipID); err != nil {
		return mapIdentityError(err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM tenant_membership WHERE id = $1`, membershipID); err != nil {
		return err
	}
	// Delete the account only when it has no membership left. A suite SSO
	// user can hold memberships in several tenants at once; removing them
	// from this tenant must not destroy their account (or their access in
	// other tenants). A locally created, single-membership account behaves
	// exactly as before.
	var otherMemberships int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM tenant_membership WHERE user_id = $1`,
		userID).Scan(&otherMemberships); err != nil {
		return err
	}
	if otherMemberships == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM app_user WHERE id = $1`, userID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "remove-member",
		ResourceType: "app_user",
		ResourceID:   userID,
	})
	s.publishEvent(ctx, EventRemoved, tenantID, userID)
	return nil
}

// loadMember loads one member including roles; a missing membership
// yields ErrNotFound.
func (s *Service) loadMember(ctx context.Context, tenantID, userID string) (Member, error) {
	var member Member
	var lastLogin time.Time
	var lastLoginNull bool
	var createdAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT u.id::text, u.email, u.display_name,
		        u.disabled, u.email_verified, u.is_platform_admin,
		        COALESCE(u.last_login_at, to_timestamp(0)),
		        u.last_login_at IS NULL,
		        u.created_at,
		        m.scope, COALESCE(m.scope_id::text, '')
		 FROM tenant_membership m
		 JOIN app_user u ON u.id = m.user_id
		 WHERE m.tenant_id = $1 AND u.id = $2`,
		tenantID, userID).Scan(&member.UserID, &member.Email, &member.DisplayName,
		&member.Disabled, &member.EmailVerified, &member.PlatformAdmin,
		&lastLogin, &lastLoginNull, &createdAt,
		&member.DataScope, &member.DataScopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("load member: %w", err)
	}
	if !lastLoginNull {
		member.LastLoginAt = lastLogin.UTC().Format(time.RFC3339)
	}
	member.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	roles, err := s.loadRoles(ctx, userID, tenantID)
	if err != nil {
		return Member{}, err
	}
	member.Roles = roles
	if err := s.resolveScopeName(ctx, tenantID, &member); err != nil {
		return Member{}, err
	}
	return member, nil
}

// resolveScopeName fills the human-readable label of a narrowed member's
// anchor. A TENANT scope has no anchor, and an anchor that has since been
// deleted leaves the label empty while the id is still reported — the UI shows
// the id, which is what an operator needs to repair the scope.
func (s *Service) resolveScopeName(ctx context.Context,
	tenantID string, member *Member) error {
	if member.DataScope == rbac.ScopeTenant || member.DataScopeID == "" {
		member.DataScopeName = ""
		return nil
	}
	table := "customer"
	if member.DataScope == rbac.ScopeGroup {
		table = "device_group"
	}
	// The table name is chosen from a closed set by the validated scope
	// level, never from request input.
	var name string
	err := s.pool.QueryRow(ctx,
		`SELECT name FROM `+table+` WHERE id = $1::uuid AND tenant_id = $2`,
		member.DataScopeID, tenantID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		member.DataScopeName = ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve data scope name: %w", err)
	}
	member.DataScopeName = name
	return nil
}

// loadRoles returns the role keys of one membership in stable order.
func (s *Service) loadRoles(ctx context.Context, userID, tenantID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mr.role
		 FROM membership_role mr
		 JOIN tenant_membership m ON m.id = mr.membership_id
		 WHERE m.user_id = $1 AND m.tenant_id = $2
		 ORDER BY mr.role`, userID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load roles: %w", err)
	}
	defer rows.Close()
	roles := []string{}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// attachRole attaches one additional role idempotently.
func (s *Service) attachRole(ctx context.Context, userID, tenantID, role string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO membership_role (id, membership_id, role)
		 SELECT gen_random_uuid(), m.id, $3
		 FROM tenant_membership m
		 WHERE m.user_id = $1 AND m.tenant_id = $2
		 ON CONFLICT (membership_id, role) DO NOTHING`,
		userID, tenantID, role)
	if err != nil {
		return fmt.Errorf("attach role: %w", err)
	}
	return nil
}

// requireTenant verifies the tenant exists.
func (s *Service) requireTenant(ctx context.Context, tenantID string) error {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tenant WHERE id = $1)`, tenantID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check tenant: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// requireNotSelf prevents an operator deleting their own account,
// which would orphan the console session; the caller identity comes
// from the request principal.
func (s *Service) requireNotSelf(ctx context.Context, userID string) error {
	if actor := actorFromContext(ctx); actor != "" && actor == userID {
		return invalid("you cannot remove your own account")
	}
	return nil
}

// actorFromContext reads the optional actor marker set by handlers.
func actorFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(actorContextKey{}).(string); ok {
		return value
	}
	return ""
}

// actorContextKey is the request-actor marker type.
type actorContextKey struct{}

// ContextWithActor marks ctx with the authenticated operator id so
// self-targeting protections work inside the service.
func ContextWithActor(ctx context.Context, actorID string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actorID)
}

// normalizeRoles de-duplicates and defaults roles.
func normalizeRoles(roles []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		out = append(out, role)
	}
	if len(out) == 0 {
		return []string{rbac.RoleTenantAdmin}
	}
	return out
}

// passwordAlphabet excludes ambiguous characters (0/O, 1/l/I) so a
// generated password reads correctly when handed to an operator.
const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// generatedPasswordLength satisfies the 10-character minimum with
// margin for future complexity rules.
const generatedPasswordLength = 16

// generatePassword draws a random password from the unambiguous
// alphabet using crypto/rand.
func generatePassword() (string, error) {
	out := make([]byte, generatedPasswordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// record appends a best-effort audit event; audit failure never
// blocks the management operation but is surfaced as an error log.
func (s *Service) record(ctx context.Context, input audit.Input) {
	if s.auditor == nil {
		return
	}
	if input.ActorID == "" {
		input.ActorID = actorFromContext(ctx)
	}
	if _, err := s.auditor.Append(ctx, input); err != nil {
		slog.Error("record member audit event failed",
			"resource", input.ResourceID, "action", input.Action, "error", err)
	}
}

// SetDataScope changes the data scope of one member's membership.
//
// The scope and the anchor move together in a single call because a narrowing
// is only meaningful together with what it points at: a GROUP scope without a
// group, or a leftover anchor on a TENANT scope, would both be misreadings
// rather than states. The anchor is verified against the same tenant by the
// rbac layer before the write, so a scope can never be pointed at another
// tenant's customer or group.
//
// This is a permission event: widening a member from CUSTOMER to TENANT grants
// reach over every device in the tenant, and the previous value is recorded.
func (s *Service) SetDataScope(ctx context.Context, tenantID, userID,
	scope, scopeID string) (Member, error) {
	scope = strings.TrimSpace(scope)
	scopeID = strings.TrimSpace(scopeID)
	if err := rbac.ValidateScope(scope, scopeID); err != nil {
		return Member{}, invalid(err.Error())
	}
	before, err := s.loadMember(ctx, tenantID, userID)
	if err != nil {
		return Member{}, err
	}
	if before.PlatformAdmin {
		return Member{}, ErrProtected
	}
	if err := rbac.SetDataScope(ctx, s.pool, userID, tenantID,
		scope, scopeID); err != nil {
		return Member{}, mapScopeError(err)
	}

	s.record(ctx, audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventPermission,
		Action:       "set-data-scope",
		ResourceType: "app_user",
		ResourceID:   userID,
		Details: map[string]any{
			"from_scope": before.DataScope, "from_scope_id": before.DataScopeID,
			"to_scope": scope, "to_scope_id": scopeID,
		},
	})
	s.publishEvent(ctx, EventDataScope, tenantID, userID)
	return s.loadMember(ctx, tenantID, userID)
}

// mapScopeError translates the rbac scope sentinels into the API's error
// vocabulary: an unknown anchor is a bad request (the caller named something
// that does not exist in this tenant), while a missing membership is a 404.
func mapScopeError(err error) error {
	switch {
	case errors.Is(err, rbac.ErrNoMembership):
		return ErrNotFound
	case errors.Is(err, rbac.ErrNoScopeAnchor):
		return invalid(err.Error())
	default:
		return err
	}
}

// mapIdentityError translates identity package errors to usermgmt
// sentinels so gateway handlers can map status codes uniformly.
func mapIdentityError(err error) error {
	switch {
	case errors.Is(err, identity.ErrUserNotFound):
		return ErrNotFound
	case errors.Is(err, identity.ErrInvalidArgument), errors.Is(err, identity.ErrInvalidPassword):
		return fmt.Errorf("%w: %s", ErrInvalidArgument, err.Error())
	default:
		return err
	}
}

// invalid wraps a validation message.
func invalid(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, message)
}
