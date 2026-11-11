// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// autoprovision_db_test.go verifies the gateway auto-provisioning batch core
// (ADR-0026/ADR-0013) against an opt-in PostgreSQL database: creation bound to
// the gateway, name-idempotent re-discovery, the per-cycle cap, rejection of
// names owned by another gateway, and the tenant/gateway guard.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const autoProvTestTenant = "00000000-0000-0000-0000-00000000d721"

// resetAutoProv wipes all test rows, ownership history and the tenant so the
// test is independently rerunnable.
func resetAutoProv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`DELETE FROM device_ownership_event WHERE tenant_id = $1`, autoProvTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM device WHERE tenant_id = $1`, autoProvTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM tenant_usage_daily WHERE tenant_id = $1`, autoProvTestTenant); err != nil {
		t.Logf("usage cleanup: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM audit_event WHERE tenant_id = $1`, autoProvTestTenant); err != nil {
		t.Logf("audit cleanup: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM tenant WHERE id = $1`, autoProvTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'autoprov-test')`, autoProvTestTenant); err != nil {
		t.Fatal(err)
	}
}

// TestAutoProvisionSubdevices exercises create, idempotent re-discovery, cap
// enforcement and cross-gateway name rejection.
func TestAutoProvisionSubdevices(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetAutoProv(t, ctx, pool)
	t.Cleanup(func() { resetAutoProv(t, ctx, pool) })

	gateway, err := admin.CreateDevice(ctx, autoProvTestTenant, DeviceDTO{
		CanonicalName: "edge-gw",
		State:         StateActive,
		IsGateway:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := admin.CreateDevice(ctx, autoProvTestTenant, DeviceDTO{
		CanonicalName: "other-gw",
		State:         StateActive,
		IsGateway:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// First discovery: two new devices created and bound to the gateway.
	res, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, gateway.ID,
		[]ProvisionCandidate{
			{LocalID: "modbus-1"},
			{LocalID: "ble-2"},
		}, 100, "gateway:"+gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Provisioned) != 2 || len(res.Rejected) != 0 {
		t.Fatalf("want 2 provisioned, got %+v", res)
	}
	for _, p := range res.Provisioned {
		if p.Existing || p.DeviceID == "" {
			t.Fatalf("new device must carry UUID and Existing=false: %+v", p)
		}
		ok, err := admin.AssertGatewaySubdevice(ctx, autoProvTestTenant, gateway.ID, p.DeviceID)
		if err != nil || !ok {
			t.Fatalf("provisioned device %s not bound to gateway: ok=%v err=%v", p.DeviceID, ok, err)
		}
	}

	// Second discovery of the same locals is idempotent: both resolve as
	// existing with stable UUIDs, no new rows, deterministic canonical names.
	first := map[string]ProvisionedDevice{}
	for _, p := range res.Provisioned {
		first[p.LocalID] = p
	}
	res2, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, gateway.ID,
		[]ProvisionCandidate{
			{LocalID: "modbus-1"},
			{LocalID: "ble-2"},
		}, 100, "gateway:"+gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Provisioned) != 2 {
		t.Fatalf("want 2 resolved, got %+v", res2)
	}
	for _, p := range res2.Provisioned {
		if !p.Existing {
			t.Fatalf("re-discovery must be Existing: %+v", p)
		}
		if want := first[p.LocalID].DeviceID; p.DeviceID != want {
			t.Fatalf("idempotency broken: want %s got %s", want, p.DeviceID)
		}
		if want := "edge-gw/" + p.LocalID; p.CanonicalName != want {
			t.Fatalf("canonical name: want %s got %s", want, p.CanonicalName)
		}
	}

	// Per-cycle cap: three new locals with cap 2 -> 2 created, 1 rejected.
	res3, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, gateway.ID,
		[]ProvisionCandidate{
			{LocalID: "n1"}, {LocalID: "n2"}, {LocalID: "n3"},
		}, 2, "gateway:"+gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.Provisioned) != 2 || len(res3.Rejected) != 1 {
		t.Fatalf("cap: want 2 provisioned/1 rejected, got %+v", res3)
	}

	// A deterministic name already owned (bound) by another gateway rejects
	// rather than stealing the binding.
	if _, err := admin.CreateDevice(ctx, autoProvTestTenant, DeviceDTO{
		CanonicalName: "edge-gw/foreign",
		State:         StateActive,
		GatewayID:     other.ID,
	}); err != nil {
		t.Fatal(err)
	}
	res4, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, gateway.ID,
		[]ProvisionCandidate{{LocalID: "foreign"}}, 100, "gateway:"+gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res4.Provisioned) != 0 || len(res4.Rejected) != 1 {
		t.Fatalf("cross-gateway name: want 1 reject, got %+v", res4)
	}

	// Missing local id is a per-candidate rejection, not a batch failure.
	res5, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, gateway.ID,
		[]ProvisionCandidate{{LocalID: ""}, {LocalID: "ok1"}}, 100, "gw")
	if err != nil {
		t.Fatal(err)
	}
	if len(res5.Provisioned) != 1 || len(res5.Rejected) != 1 {
		t.Fatalf("empty local: want partial success, got %+v", res5)
	}
}

// TestAutoProvisionGuardRejectsNonGateway ensures only an ACTIVE gateway of
// the tenant may be a provisioning target.
func TestAutoProvisionGuardRejectsNonGateway(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetAutoProv(t, ctx, pool)
	t.Cleanup(func() { resetAutoProv(t, ctx, pool) })

	plain, err := admin.CreateDevice(ctx, autoProvTestTenant, DeviceDTO{
		CanonicalName: "plain-device",
		State:         StateActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.AutoProvisionSubdevices(ctx, autoProvTestTenant, plain.ID,
		[]ProvisionCandidate{{LocalID: "x"}}, 10, "gw"); err == nil {
		t.Fatal("want error provisioning under a non-gateway")
	}
}
