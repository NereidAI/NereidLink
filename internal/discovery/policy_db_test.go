// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy_db_test.go verifies migration 0045 discovery_policy: tenant-default
// and gateway overrides, tri-state inheritance, caps validation, the unique
// tenant-default constraint and cascading deletes.

package discovery

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const policyTestTenant = "00000000-0000-0000-0000-00000000d805"

func newPolicyStore(t *testing.T) (*PolicyStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewPolicyStore(pool), pool
}

func resetPolicyFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM discovery_policy WHERE tenant_id=$1`, policyTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name IN ('dp-gw','dp-gw2')`)
	_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, policyTestTenant)
}

func mkGateway(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway, state)
		 VALUES (gen_random_uuid(),$1,$2,TRUE,'ACTIVE') RETURNING id::text`,
		policyTestTenant, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPolicyStoreTenantAndGateway(t *testing.T) {
	store, pool := newPolicyStore(t)
	resetPolicyFixture(t, pool)
	t.Cleanup(func() { resetPolicyFixture(t, pool) })
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'dp-test') ON CONFLICT DO NOTHING`,
		policyTestTenant); err != nil {
		t.Fatal(err)
	}
	gw := mkGateway(t, pool, "dp-gw")

	// No rows yet: both nil.
	tr, gr, err := store.RowsFor(ctx, policyTestTenant, gw)
	if err != nil || tr != nil || gr != nil {
		t.Fatalf("fresh scope must have no rows: tr=%v gr=%v err=%v", tr, gr, err)
	}

	// Tenant default enables and sets a profile.
	if _, err := store.SetTenant(ctx, policyTestTenant, Override{
		Enabled: boolp(true), DefaultProfile: strp("base"),
	}); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	tr, gr, err = store.RowsFor(ctx, policyTestTenant, gw)
	if err != nil || tr == nil || tr.Enabled == nil || !*tr.Enabled ||
		tr.DefaultProfile == nil || *tr.DefaultProfile != "base" {
		t.Fatalf("tenant row wrong: %+v err=%v", tr, err)
	}
	if gr != nil {
		t.Fatal("gateway override must still be absent")
	}

	// Gateway overrides only the cycle cap; other fields stay nil (inherit).
	if _, err := store.SetGateway(ctx, policyTestTenant, gw, Override{
		MaxPerCycle: intp(7),
	}); err != nil {
		t.Fatalf("set gateway: %v", err)
	}
	_, gr, _ = store.RowsFor(ctx, policyTestTenant, gw)
	if gr == nil || gr.MaxPerCycle == nil || *gr.MaxPerCycle != 7 || gr.Enabled != nil {
		t.Fatalf("gateway override must set only cap: %+v", gr)
	}

	// Effective resolution folds fallback <- tenant <- gateway.
	eff, err := EffectivePolicy(ctx, store,
		Policy{Enabled: false, MaxPerCycle: 100, MaxCandidates: 1000},
		policyTestTenant, gw)
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Enabled || eff.MaxPerCycle != 7 || eff.DefaultProfile != "base" ||
		eff.MaxCandidates != 1000 {
		t.Fatalf("effective policy wrong: %+v", eff)
	}

	// Non-positive cap is rejected.
	if _, err := store.SetGateway(ctx, policyTestTenant, gw, Override{MaxPerCycle: intp(0)}); !errors.Is(err, ErrInvalidCap) {
		t.Fatalf("cap 0 must be ErrInvalidCap, got %v", err)
	}

	// Unknown gateway is rejected.
	if _, err := store.SetGateway(ctx, policyTestTenant,
		"00000000-0000-0000-0000-0000000000ff", Override{Enabled: boolp(true)}); !errors.Is(err, ErrGatewayUnknown) {
		t.Fatalf("foreign gateway must be ErrGatewayUnknown, got %v", err)
	}

	// Deleting the gateway cascade-removes its override.
	if _, err := pool.Exec(ctx, `DELETE FROM device WHERE id=$1`, gw); err != nil {
		t.Fatal(err)
	}
	_, gr, _ = store.RowsFor(ctx, policyTestTenant, gw)
	if gr != nil {
		t.Fatal("gateway override must cascade with device")
	}
}

func TestPolicyStoreTenantDefaultUnique(t *testing.T) {
	store, pool := newPolicyStore(t)
	resetPolicyFixture(t, pool)
	t.Cleanup(func() { resetPolicyFixture(t, pool) })
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'dp-uniq') ON CONFLICT DO NOTHING`,
		policyTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTenant(ctx, policyTestTenant, Override{Enabled: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	// A second tenant-default row (bypassing the upsert) must violate the
	// partial unique index.
	_, err := pool.Exec(ctx,
		`INSERT INTO discovery_policy (tenant_id, gateway_id, enabled)
		 VALUES ($1, NULL, false)`, policyTestTenant)
	if err == nil {
		t.Fatal("two tenant-default rows must be rejected")
	}
}
