// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go loads membership grants for a user and builds the principal
// used for gateway authorization.

package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/identity"
)

// ErrUserNotFound means a principal cannot be built because the user row
// no longer exists.
var ErrUserNotFound = errors.New("rbac: user not found")

// Resolver builds principals from verified identity claims.
type Resolver struct {
	pool *pgxpool.Pool
}

// NewResolver constructs the principal resolver.
func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

// PrincipalForUser builds a human principal directly from a verified user
// id. User API-key authentication uses it after the credential itself has
// been checked; membership and data-scope loading remain owned by RBAC.
func (r *Resolver) PrincipalForUser(ctx context.Context, userID string) (Principal, error) {
	var email string
	var platformAdmin bool
	err := r.pool.QueryRow(ctx,
		`SELECT email, is_platform_admin FROM app_user WHERE id = $1::uuid`,
		userID).Scan(&email, &platformAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUserNotFound
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load API-key owner: %w", err)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT m.tenant_id::text, m.scope, COALESCE(m.scope_id::text, ''),
		        mr.role
		 FROM tenant_membership m
		 JOIN membership_role mr ON mr.membership_id = m.id
		 WHERE m.user_id = $1::uuid
		 ORDER BY m.created_at`, userID)
	if err != nil {
		return Principal{}, fmt.Errorf("load memberships: %w", err)
	}
	defer rows.Close()

	principal := Principal{
		UserID:        userID,
		Email:         email,
		PlatformAdmin: platformAdmin,
		Tenants:       map[string]TenantGrant{},
	}
	for rows.Next() {
		var tenantID, scope, scopeID, role string
		if err := rows.Scan(&tenantID, &scope, &scopeID, &role); err != nil {
			return Principal{}, err
		}
		grant := principal.Tenants[tenantID]
		if grant.Scope == "" {
			grant.Scope = scope
			grant.ScopeID = scopeID
		} else if wider := WidestScope(grant.Scope, scope); wider != grant.Scope {
			grant.Scope = wider
			grant.ScopeID = scopeID
		}
		grant.Roles = append(grant.Roles, role)
		principal.Tenants[tenantID] = grant
	}
	return principal, rows.Err()
}

// PermissionPointsForUser returns the owner's current permission points in
// one tenant. The tenant-admin wildcard is retained as PointAll.
func (r *Resolver) PermissionPointsForUser(ctx context.Context,
	userID, tenantID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT mr.role
		 FROM tenant_membership m
		 JOIN membership_role mr ON mr.membership_id = m.id
		 WHERE m.user_id = $1::uuid AND m.tenant_id = $2::uuid`,
		userID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load user permission points: %w", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	points := make([]string, 0)
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		for _, point := range rolePoints[role] {
			if !seen[point] {
				seen[point] = true
				points = append(points, point)
			}
		}
	}
	return points, rows.Err()
}

// FromToken verifies an access token through the identity service and
// loads the caller's membership grants.
func (r *Resolver) FromToken(ctx context.Context,
	auth *identity.Service, token string) (Principal, error) {
	claims, err := auth.ParseAccessToken(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return r.fromClaims(ctx, claims)
}

// FromClaims builds a principal from already verified claims, sharing
// the membership query with the WebSocket path (which authenticates
// before upgrading).
func (r *Resolver) FromClaims(ctx context.Context,
	claims identity.Claims) (Principal, error) {
	return r.fromClaims(ctx, claims)
}

// fromClaims loads rows and assembles the principal.
func (r *Resolver) fromClaims(ctx context.Context,
	claims identity.Claims) (Principal, error) {
	// A machine token carries its authorization in the token itself
	// (scopes) and owns no membership row, so the human membership query
	// is skipped entirely. Its single tenant grant uses the
	// SERVICE_ACCOUNT label purely for audit/telemetry; HasPoint reads
	// Principal.Scopes, not that role.
	if claims.Machine {
		tenants := map[string]TenantGrant{}
		if claims.TenantID != "" {
			tenants[claims.TenantID] = TenantGrant{
				Scope: ScopeTenant,
				Roles: []string{RoleServiceAccount},
			}
		}
		return Principal{
			UserID:  claims.Subject,
			Email:   claims.Email,
			Tenants: tenants,
			Machine: true,
			Scopes:  claims.Scopes,
		}, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT m.tenant_id::text, m.scope, COALESCE(m.scope_id::text, ''),
		        mr.role
		 FROM tenant_membership m
		 JOIN membership_role mr ON mr.membership_id = m.id
		 WHERE m.user_id = $1
		 ORDER BY m.created_at`,
		claims.Subject)
	if err != nil {
		return Principal{}, fmt.Errorf("load memberships: %w", err)
	}
	defer rows.Close()

	principal := Principal{
		UserID:        claims.Subject,
		Email:         claims.Email,
		PlatformAdmin: claims.PlatformAdmin,
		Tenants:       map[string]TenantGrant{},
	}
	// Membership rows are joined to membership_role, so a member holding two
	// roles yields one row per role. The tenant grant is therefore rebuilt
	// for each row and must be merged, not overwritten: assigning the scope on
	// every iteration would let whichever role happened to be read last decide
	// the narrowing. The widest scope wins, so a member holding both a TENANT
	// and a CUSTOMER role is never narrowed below the access their roles
	// imply. A single membership row currently carries one scope shared by all
	// of its roles, so this is defensive rather than observable today.
	for rows.Next() {
		var tenantID, scope, scopeID, role string
		if err := rows.Scan(&tenantID, &scope, &scopeID, &role); err != nil {
			return Principal{}, err
		}
		grant := principal.Tenants[tenantID]
		// The first row for a tenant adopts its scope as-is; only a
		// subsequent row is a merge candidate. WidestScope treats an empty
		// level as TENANT, so it must not see the not-yet-set first level.
		if grant.Scope == "" {
			grant.Scope = scope
			grant.ScopeID = scopeID
		} else if wider := WidestScope(grant.Scope, scope); wider != grant.Scope {
			grant.Scope = wider
			grant.ScopeID = scopeID
		}
		grant.Roles = append(grant.Roles, role)
		principal.Tenants[tenantID] = grant
	}
	return principal, rows.Err()
}
