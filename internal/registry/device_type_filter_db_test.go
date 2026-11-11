// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// device_type_filter_db_test.go verifies the device-list Type filter and the
// created-at-descending default ordering against a real PostgreSQL. The three
// console kinds are derived, not stored: gateway (is_gateway), subdevice
// (non-gateway bound to a gateway) and standalone device (non-gateway,
// unbound), so the SQL predicate must classify them from both columns.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

const deviceTypeTestTenant = "00000000-0000-0000-0000-00000000d741"

func resetDeviceTypeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, _ = pool.Exec(ctx, `DELETE FROM device_ownership_event WHERE tenant_id=$1`, deviceTypeTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device WHERE tenant_id=$1`, deviceTypeTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM audit_event WHERE tenant_id=$1`, deviceTypeTestTenant)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'device-type-test')
		 ON CONFLICT (id) DO NOTHING`, deviceTypeTestTenant); err != nil {
		t.Fatal(err)
	}
}

func TestListDevicesTypeFilterAndOrdering(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(deviceTypeTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetDeviceTypeFixture(t, ctx, pool)
	t.Cleanup(func() { resetDeviceTypeFixture(t, context.Background(), pool) })

	// Insert out of name/created order with explicit, spaced created_at so the
	// ordering assertion cannot pass on equal timestamps.
	insert := func(name string, isGateway bool, gatewayID any, createdAt string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state,
			                     is_gateway, gateway_id, created_at)
			 VALUES ($1,$2,$3,'ACTIVE',$4,$5,$6)`,
			id, deviceTypeTestTenant, name, isGateway, gatewayID, createdAt); err != nil {
			t.Fatal(err)
		}
		return id
	}
	standalone := insert("zzz-standalone", false, nil, "2026-01-01 00:00:00+00")
	gw := insert("aaa-gateway", true, nil, "2026-01-03 00:00:00+00")
	sub := insert("mmm-sub", false, gw, "2026-01-02 00:00:00+00")

	list := func(kind string) []DeviceDTO {
		t.Helper()
		page, err := admin.ListDevices(ctx, deviceTypeTestTenant,
			DeviceFilter{Type: kind}, 1, 50)
		if err != nil {
			t.Fatalf("ListDevices type=%q: %v", kind, err)
		}
		return page.Items
	}

	got := func(kind string) map[string]bool {
		out := map[string]bool{}
		for _, d := range list(kind) {
			out[d.ID] = true
		}
		return out
	}

	gws := got("gateway")
	if !gws[gw] || len(gws) != 1 {
		t.Fatalf("gateway filter must return only the gateway, got %v", gws)
	}
	subs := got("subdevice")
	if !subs[sub] || len(subs) != 1 {
		t.Fatalf("subdevice filter must return only the bound non-gateway, got %v", subs)
	}
	devs := got("device")
	if !devs[standalone] || len(devs) != 1 {
		t.Fatalf("device filter must return only the standalone device, got %v", devs)
	}

	// No type filter: newest created_at first, regardless of name.
	all := list("")
	if len(all) != 3 {
		t.Fatalf("unfiltered list must return all three, got %d", len(all))
	}
	wantOrder := []string{gw, sub, standalone}
	for i, want := range wantOrder {
		if all[i].ID != want {
			t.Fatalf("position %d: want %s, list is %v", i, want,
				[]string{all[0].ID, all[1].ID, all[2].ID})
		}
	}
}
