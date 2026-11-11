// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// group_target_db_test.go covers ADR-0019 device-group escalation targets
// against an opt-in PostgreSQL database. It asserts that a group-scoped
// step reaches exactly the role holders whose RBAC data scope covers the
// target group's subtree, and that a cross-tenant or dangling target is
// rejected.

package escalation

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scopeFixture holds the ids of a group tree and customer used by the
// scoped-recipient test.
type scopeFixture struct {
	tenantID string
	rootID   string
	childA   string
	childB   string
	custID   string
}

// seedScopeFixture inserts a customer and a two-child group tree under
// the tenant, plus one user+membership+role per requested scope. It
// returns the fixture and a cleanup function.
func seedScopeFixture(t *testing.T, pool *pgxpool.Pool, tenantID string) (scopeFixture, func()) {
	t.Helper()
	ctx := context.Background()
	fx := scopeFixture{
		tenantID: tenantID,
		rootID:   uuid.NewString(),
		childA:   uuid.NewString(),
		childB:   uuid.NewString(),
		custID:   uuid.NewString(),
	}

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v", err)
		}
	}
	mustExec(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,$3)`,
		fx.custID, tenantID, "scope-cust-"+fx.custID)
	mustExec(`INSERT INTO device_group (id, tenant_id, parent_id, name) VALUES ($1,$2,NULL,$3)`,
		fx.rootID, tenantID, "root")
	mustExec(`INSERT INTO device_group (id, tenant_id, parent_id, customer_id, name) VALUES ($1,$2,$3,$4,$5)`,
		fx.childA, tenantID, fx.rootID, fx.custID, "child-a")
	mustExec(`INSERT INTO device_group (id, tenant_id, parent_id, name) VALUES ($1,$2,$3,$4)`,
		fx.childB, tenantID, fx.rootID, "child-b")

	cleanup := func() {
		background := context.Background()
		// Memberships and roles cascade from app_user.
		if _, err := pool.Exec(background,
			`DELETE FROM app_user WHERE tenant_id = $1 AND email LIKE 'scope-%'`, tenantID); err != nil {
			t.Logf("cleanup users: %v", err)
		}
		// Children before parents.
		if _, err := pool.Exec(background,
			`DELETE FROM device_group WHERE parent_id IS NOT NULL AND tenant_id = $1`, tenantID); err != nil {
			t.Logf("cleanup child groups: %v", err)
		}
		if _, err := pool.Exec(background,
			`DELETE FROM device_group WHERE tenant_id = $1`, tenantID); err != nil {
			t.Logf("cleanup groups: %v", err)
		}
		if _, err := pool.Exec(background,
			`DELETE FROM customer WHERE tenant_id = $1`, tenantID); err != nil {
			t.Logf("cleanup customers: %v", err)
		}
	}
	return fx, cleanup
}

// addScopedMember inserts one user, membership and role and returns the
// email. scopeID is the customer/group anchor, empty for TENANT.
func addScopedMember(t *testing.T, pool *pgxpool.Pool, tenantID, email, role, scope, scopeID string) string {
	t.Helper()
	ctx := context.Background()
	userID := uuid.NewString()
	membershipID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, display_name) VALUES ($1,$2,$3,$4)`,
		userID, tenantID, email, email); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var scopeArg any
	if scopeID != "" {
		scopeArg = scopeID
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant_membership (id, user_id, tenant_id, scope, scope_id) VALUES ($1,$2,$3,$4,$5)`,
		membershipID, userID, tenantID, scope, scopeArg); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO membership_role (id, membership_id, role) VALUES ($1,$2,$3)`,
		uuid.NewString(), membershipID, role); err != nil {
		t.Fatalf("insert role: %v", err)
	}
	return email
}

// TestGroupTargetScopesRecipients verifies a group-scoped step reaches
// only the role holders whose data scope covers the target subtree.
func TestGroupTargetScopesRecipients(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, deviceID, cleanupFixture := seedEscalationFixture(t, pool)
	fx, cleanupScope := seedScopeFixture(t, pool, tenantID)
	defer cleanupFixture()
	defer cleanupScope()

	// Included: tenant-wide, ancestor-scoped, self-scoped, customer anchor.
	want := []string{}
	want = append(want, addScopedMember(t, pool, tenantID, "scope-tenant@example.com", "ENGINEER", "TENANT", ""))
	want = append(want, addScopedMember(t, pool, tenantID, "scope-root@example.com", "ENGINEER", "GROUP", fx.rootID))
	want = append(want, addScopedMember(t, pool, tenantID, "scope-childa@example.com", "ENGINEER", "GROUP", fx.childA))
	want = append(want, addScopedMember(t, pool, tenantID, "scope-cust@example.com", "ENGINEER", "CUSTOMER", fx.custID))
	// Excluded: sibling subtree, wrong role, disabled.
	addScopedMember(t, pool, tenantID, "scope-sibling@example.com", "ENGINEER", "GROUP", fx.childB)
	addScopedMember(t, pool, tenantID, "scope-readonly@example.com", "READ_ONLY", "TENANT", "")
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, display_name, disabled) VALUES ($1,$2,$3,$4,true)`,
		uuid.NewString(), tenantID, "scope-disabled@example.com", "disabled"); err != nil {
		t.Fatalf("insert disabled user: %v", err)
	}
	disabledMembership := uuid.NewString()
	var disabledUserID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app_user WHERE tenant_id=$1 AND email=$2`,
		tenantID, "scope-disabled@example.com").Scan(&disabledUserID); err != nil {
		t.Fatalf("load disabled user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant_membership (id, user_id, tenant_id, scope) VALUES ($1,$2,$3,'TENANT')`,
		disabledMembership, disabledUserID, tenantID); err != nil {
		t.Fatalf("insert disabled membership: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO membership_role (id, membership_id, role) VALUES ($1,$2,'ENGINEER')`,
		uuid.NewString(), disabledMembership); err != nil {
		t.Fatalf("insert disabled role: %v", err)
	}

	chain, err := svc.CreateChain(ctx, tenantID, SaveInput{
		Name:    "group-chain-" + uuid.NewString(),
		Enabled: boolPtr(true),
		Steps: []StepInput{{
			DelaySeconds:  0,
			TargetRole:    "ENGINEER",
			TargetGroupID: fx.childA,
			StopOnAck:     true,
			StopOnClear:   true,
		}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if chain.Steps[0].TargetGroupID != fx.childA {
		t.Fatalf("chain step group = %q, want %q", chain.Steps[0].TargetGroupID, fx.childA)
	}

	insertAlarmWithChain(t, pool, tenantID, deviceID, chain.ID, "ACTIVE", time.Now().Add(-time.Minute))

	sender := &fakeSender{}
	runner := NewRunner(pool, sender, &fakeInbox{}, time.Second)
	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(sender.sent))
	}

	got := append([]string(nil), sender.sent[0].Recipients...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recipients = %v, want %v", got, want)
		}
	}

	cleanupChain(t, pool, tenantID, chain.ID)
}

// TestGroupTargetRejectsForeignTenant verifies a target group that is
// missing or owned by another tenant is rejected with ErrInvalid.
func TestGroupTargetRejectsForeignTenant(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, _, cleanupFixture := seedEscalationFixture(t, pool)
	foreignTenant := "00000000-0000-0000-0000-00000000e502"
	foreignGroup := uuid.NewString()
	defer cleanupFixture()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'foreign-tenant')
		 ON CONFLICT (id) DO NOTHING`, foreignTenant); err != nil {
		t.Fatalf("insert foreign tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_group (id, tenant_id, name) VALUES ($1,$2,'foreign-group')`,
		foreignGroup, foreignTenant); err != nil {
		t.Fatalf("insert foreign group: %v", err)
	}
	defer func() {
		background := context.Background()
		if _, err := pool.Exec(background, `DELETE FROM device_group WHERE tenant_id=$1`, foreignTenant); err != nil {
			t.Logf("cleanup foreign group: %v", err)
		}
		if _, err := pool.Exec(background, `DELETE FROM tenant WHERE id=$1`, foreignTenant); err != nil {
			t.Logf("cleanup foreign tenant: %v", err)
		}
	}()

	for name, groupID := range map[string]string{
		"foreign":  foreignGroup,
		"dangling": uuid.NewString(),
	} {
		_, err := svc.CreateChain(ctx, tenantID, SaveInput{
			Name: "bad-group-" + uuid.NewString(),
			Steps: []StepInput{{
				DelaySeconds:  0,
				TargetRole:    "ENGINEER",
				TargetGroupID: groupID,
			}},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s group target: err = %v, want ErrInvalid", name, err)
		}
	}
}
