// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_db_test.go proves delegation safety against a real PostgreSQL:
// a platform admin target is protected from every member mutation, and
// removing a multi-tenant member leaves the account and other memberships
// intact. Skipped without NL_TEST_DATABASE_URL.

package usermgmt

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mgmtFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	svc    *Service
	tenant string
}

func newMgmtFixture(t *testing.T) *mgmtFixture {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	fx := &mgmtFixture{t: t, pool: pool, svc: New(pool), tenant: uuid.NewString()}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name, status) VALUES ($1, 'UM Test', 'ACTIVE')`,
		fx.tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`WITH d AS (
			    DELETE FROM tenant_usage_daily WHERE tenant_id = $1 RETURNING 1
			) DELETE FROM tenant WHERE id = $1`, fx.tenant)
	})
	return fx
}

// insertUser creates an app_user with one membership in tenant carrying
// roles; platform flags the cross-tenant admin.
func (fx *mgmtFixture) insertUser(platform bool, roles ...string) string {
	ctx := context.Background()
	id := uuid.NewString()
	home := any(nil)
	if !platform {
		home = fx.tenant
	}
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, display_name, email_verified, is_platform_admin)
		 VALUES ($1, $2, $3, 'UM Test', TRUE, $4)`,
		id, home, "um-"+id[:8]+"@example.com", platform); err != nil {
		fx.t.Fatalf("create user: %v", err)
	}
	fx.t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM app_user WHERE id = $1`, id)
	})
	var mid uuid.UUID
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO tenant_membership (id, user_id, tenant_id, scope)
		 VALUES (gen_random_uuid(), $1, $2, 'TENANT') RETURNING id`,
		id, fx.tenant).Scan(&mid); err != nil {
		fx.t.Fatalf("create membership: %v", err)
	}
	for _, role := range roles {
		if _, err := fx.pool.Exec(ctx,
			`INSERT INTO membership_role (id, membership_id, role)
			 VALUES (gen_random_uuid(), $1, $2)`, mid, role); err != nil {
			fx.t.Fatalf("attach role: %v", err)
		}
	}
	return id
}

func TestMemberMutationsProtectPlatformAdmin(t *testing.T) {
	fx := newMgmtFixture(t)
	ctx := context.Background()
	admin := fx.insertUser(true, "TENANT_ADMIN")

	if _, err := fx.svc.SetRoles(ctx, fx.tenant, admin, []string{"READ_ONLY"}); !errors.Is(err, ErrProtected) {
		t.Errorf("SetRoles error = %v, want ErrProtected", err)
	}
	if _, err := fx.svc.SetDataScope(ctx, fx.tenant, admin, "TENANT", ""); !errors.Is(err, ErrProtected) {
		t.Errorf("SetDataScope error = %v, want ErrProtected", err)
	}
	if _, err := fx.svc.ResetPassword(ctx, fx.tenant, admin, "Newpass12345!"); !errors.Is(err, ErrProtected) {
		t.Errorf("ResetPassword error = %v, want ErrProtected", err)
	}
	if _, err := fx.svc.SetDisabled(ctx, fx.tenant, admin, true); !errors.Is(err, ErrProtected) {
		t.Errorf("SetDisabled error = %v, want ErrProtected", err)
	}
	if err := fx.svc.RemoveMember(ctx, fx.tenant, admin); !errors.Is(err, ErrProtected) {
		t.Errorf("RemoveMember error = %v, want ErrProtected", err)
	}
}

func TestRemoveMemberKeepsMultiTenantAccount(t *testing.T) {
	fx := newMgmtFixture(t)
	ctx := context.Background()

	// A second tenant and a user holding memberships in both.
	other := uuid.NewString()
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO tenant (id, name, status) VALUES ($1, 'UM Test 2', 'ACTIVE')`, other); err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	fx.t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(),
			`WITH d AS (DELETE FROM tenant_usage_daily WHERE tenant_id = $1 RETURNING 1)
			 DELETE FROM tenant WHERE id = $1`, other)
	})
	user := fx.insertUser(false, "ENGINEER")
	var otherMid uuid.UUID
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO tenant_membership (id, user_id, tenant_id, scope)
		 VALUES (gen_random_uuid(), $1, $2, 'TENANT') RETURNING id`,
		user, other).Scan(&otherMid); err != nil {
		t.Fatalf("create other membership: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO membership_role (id, membership_id, role)
		 VALUES (gen_random_uuid(), $1, 'READ_ONLY')`, otherMid); err != nil {
		t.Fatalf("attach other role: %v", err)
	}

	if err := fx.svc.RemoveMember(ctx, fx.tenant, user); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	var accounts, memberships int
	if err := fx.pool.QueryRow(ctx,
		`SELECT count(*) FROM app_user WHERE id = $1`, user).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 {
		t.Errorf("account deleted despite another membership: %d rows", accounts)
	}
	if err := fx.pool.QueryRow(ctx,
		`SELECT count(*) FROM tenant_membership WHERE user_id = $1`, user).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Errorf("want 1 remaining membership, got %d", memberships)
	}

	// Removing the last membership now deletes the account.
	if err := fx.svc.RemoveMember(ctx, other, user); err != nil {
		t.Fatalf("RemoveMember other: %v", err)
	}
	if err := fx.pool.QueryRow(ctx,
		`SELECT count(*) FROM app_user WHERE id = $1`, user).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Errorf("account should be deleted with no memberships left, got %d", accounts)
	}
}
