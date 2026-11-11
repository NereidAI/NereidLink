// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// memberships.go exposes a user's tenant grants on the authenticated
// profile (/v1/auth/me and the login response). The RBAC layer owns
// enforcement from these same rows; this projection only lets the UI
// decide which tenant-scoped surfaces (for example member
// administration) to show without a second round trip.

package identity

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// attachTenantRoles fills u.TenantRoles with the built-in roles per
// membership tenant. An account without memberships (a freshly
// bootstrapped platform admin) keeps the nil map, so the field stays
// omitted.
func attachTenantRoles(ctx context.Context, pool *pgxpool.Pool, u *User) error {
	rows, err := pool.Query(ctx, `
		SELECT m.tenant_id::text, mr.role
		  FROM tenant_membership m
		  JOIN membership_role mr ON mr.membership_id = m.id
		 WHERE m.user_id = $1::uuid
		 ORDER BY m.tenant_id::text, mr.role`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, role string
		if err := rows.Scan(&tenantID, &role); err != nil {
			return err
		}
		if u.TenantRoles == nil {
			u.TenantRoles = map[string][]string{}
		}
		u.TenantRoles[tenantID] = append(u.TenantRoles[tenantID], role)
	}
	return rows.Err()
}
