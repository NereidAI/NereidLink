// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// memberships.go provides the write-side helpers used by seeding and by
// the future users management page: idempotent membership creation and
// role attachment.

package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoMembership is returned when a scope update targets a user that is not
// a member of the tenant.
var ErrNoMembership = errors.New("rbac: membership not found")

// ErrNoScopeAnchor is returned when a CUSTOMER or GROUP scope names an anchor
// that does not exist in the tenant. It is deliberately distinct from a
// generic not-found so the API can answer 400 (a bad request) rather than 404.
var ErrNoScopeAnchor = errors.New("rbac: data scope anchor not found")

// EnsureMembership creates the user's tenant membership when absent
// (TENANT scope default) and attaches role to it. Repeated calls
// converge; changing scope later goes through SetDataScope.
//
// A membership row carries exactly one data scope shared by all of its roles,
// which is why the tenant grant is merged by widest scope when the principal
// is assembled from the role join.
func EnsureMembership(ctx context.Context, pool *pgxpool.Pool,
	userID, tenantID, role string) error {
	return EnsureMembershipScoped(ctx, pool, userID, tenantID, role,
		ScopeTenant, "")
}

// EnsureMembershipScoped is EnsureMembership with an explicit initial data
// scope. An existing membership is left untouched, so provisioning is
// idempotent and never silently re-widens or re-narrows an account; use
// SetDataScope to change an existing scope deliberately.
func EnsureMembershipScoped(ctx context.Context, pool *pgxpool.Pool,
	userID, tenantID, role, scope, scopeID string) error {
	if err := ValidateScope(scope, scopeID); err != nil {
		return fmt.Errorf("membership scope: %w", err)
	}
	if err := ensureScopeAnchor(ctx, pool, tenantID, scope, scopeID); err != nil {
		return err
	}
	membershipID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant_membership (id, user_id, tenant_id, scope, scope_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid)
		 ON CONFLICT (user_id, tenant_id) DO NOTHING`,
		membershipID, userID, tenantID, scope, scopeID); err != nil {
		return fmt.Errorf("create membership: %w", err)
	}

	// Re-read the actual id: ON CONFLICT may have skipped because the
	// membership already exists.
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM tenant_membership
		 WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID).Scan(&membershipID); err != nil {
		return fmt.Errorf("read membership: %w", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO membership_role (id, membership_id, role)
		 VALUES (gen_random_uuid(), $1, $2)
		 ON CONFLICT (membership_id, role) DO NOTHING`,
		membershipID, role); err != nil {
		return fmt.Errorf("attach membership role: %w", err)
	}
	return nil
}

// SetDataScope replaces the data scope of an existing tenant membership.
//
// The anchor is verified against the same tenant before the write, so a scope
// can never point at another tenant's customer or group. Only the scope
// columns move: roles are managed separately by SetRoles, and clearing
// scope_id on the way to TENANT prevents a stale anchor from surviving a
// widening.
//
// tenant_membership carries only created_at (migration 0014), so there is no
// updated_at to stamp. The scope change is instead recorded by the caller's
// audit event; do not add an updated_at column for this without a migration.
func SetDataScope(ctx context.Context, pool *pgxpool.Pool,
	userID, tenantID, scope, scopeID string) error {
	if err := ValidateScope(scope, scopeID); err != nil {
		return fmt.Errorf("data scope: %w", err)
	}
	if err := ensureScopeAnchor(ctx, pool, tenantID, scope, scopeID); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx,
		`UPDATE tenant_membership
		 SET scope = $3, scope_id = NULLIF($4,'')::uuid
		 WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID, scope, scopeID)
	if err != nil {
		return fmt.Errorf("update membership scope: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: membership not found", ErrNoMembership)
	}
	return nil
}

// ensureScopeAnchor verifies that a CUSTOMER or GROUP scope anchor exists in
// the given tenant. TENANT needs no anchor.
//
// Verification is done in the service rather than by a foreign key because
// tenant_membership.scope_id is intentionally unconstrained: the column must
// be able to hold either a customer or a group id, so referential integrity
// has to be established per level by the application.
func ensureScopeAnchor(ctx context.Context, pool *pgxpool.Pool,
	tenantID, scope, scopeID string) error {
	if scope == ScopeTenant {
		return nil
	}
	var table string
	switch scope {
	case ScopeCustomer:
		table = "customer"
	case ScopeGroup:
		table = "device_group"
	default:
		// ValidateScope already rejected this, including SELF.
		return fmt.Errorf("unsupported data scope %q", scope)
	}
	// The table name is chosen from a closed set above, never from input.
	var owned bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM `+table+
			` WHERE id = $2::uuid AND tenant_id = $1::uuid)`,
		tenantID, scopeID).Scan(&owned)
	if err != nil {
		return fmt.Errorf("verify data scope anchor: %w", err)
	}
	if !owned {
		return fmt.Errorf("%w: data scope %s %s not found in tenant",
			ErrNoScopeAnchor, scope, scopeID)
	}
	return nil
}

// MembershipInfo describes one tenant membership of a user together with
// the roles it currently carries.
type MembershipInfo struct {
	// MembershipID is the tenant_membership primary key.
	MembershipID string
	// TenantID is the tenant the membership belongs to.
	TenantID string
	// Roles are the built-in role keys attached to the membership.
	Roles []string
}

// ListUserMemberships returns every tenant membership a user holds with
// its roles. Used by suite SSO reconciliation to revoke access that is no
// longer assigned upstream.
func ListUserMemberships(ctx context.Context, pool *pgxpool.Pool, userID string) ([]MembershipInfo, error) {
	rows, err := pool.Query(ctx, `
		SELECT m.id::text, m.tenant_id::text, COALESCE(r.roles, ARRAY[]::text[])
		  FROM tenant_membership m
		  LEFT JOIN LATERAL (
			SELECT array_agg(role ORDER BY role) AS roles
			  FROM membership_role WHERE membership_id = m.id
		  ) r ON TRUE
		 WHERE m.user_id = $1::uuid
		 ORDER BY m.tenant_id::text`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()

	var out []MembershipInfo
	for rows.Next() {
		var mi MembershipInfo
		if err := rows.Scan(&mi.MembershipID, &mi.TenantID, &mi.Roles); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out = append(out, mi)
	}
	return out, rows.Err()
}

// RemoveRoles deletes the given built-in role attachments from one
// membership and returns the number of roles remaining on it. It never
// deletes the membership itself.
func RemoveRoles(ctx context.Context, pool *pgxpool.Pool, membershipID string, roles []string) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin remove roles: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM membership_role
		 WHERE membership_id = $1 AND role = ANY($2)`,
		membershipID, roles); err != nil {
		return 0, fmt.Errorf("delete membership roles: %w", err)
	}
	var remaining int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM membership_role WHERE membership_id = $1`,
		membershipID).Scan(&remaining); err != nil {
		return 0, fmt.Errorf("count membership roles: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit remove roles: %w", err)
	}
	return remaining, nil
}

// DeleteMembership removes one membership row; its role rows cascade.
// Unlike usermgmt.RemoveMember it does not delete the app_user, because a
// suite SSO user may hold memberships in several tenants at once.
func DeleteMembership(ctx context.Context, pool *pgxpool.Pool, membershipID string) error {
	if _, err := pool.Exec(ctx,
		`DELETE FROM tenant_membership WHERE id = $1::uuid`,
		membershipID); err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	return nil
}
