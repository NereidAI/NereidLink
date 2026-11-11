// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestListDevicesStateAllIncludesDecommissioned pins the state-filter
// semantics: an empty State applies the default visible set (which hides
// DECOMMISSIONED tombstones), an explicit state filters exactly, and
// StateAll disables filtering so the console "all states" view lists
// everything including decommissioned rows.
func TestListDevicesStateAllIncludesDecommissioned(t *testing.T) {
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

	activeID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state, created_at)
		 VALUES ($1,$2,'state-active','ACTIVE','2026-01-01 00:00:00+00')`,
		activeID, deviceTypeTestTenant); err != nil {
		t.Fatal(err)
	}
	deadID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state, created_at)
		 VALUES ($1,$2,'state-dead','DECOMMISSIONED','2026-01-02 00:00:00+00')`,
		deadID, deviceTypeTestTenant); err != nil {
		t.Fatal(err)
	}

	ids := func(filter DeviceFilter) map[string]bool {
		t.Helper()
		page, err := admin.ListDevices(ctx, deviceTypeTestTenant, filter, 1, 50)
		if err != nil {
			t.Fatalf("ListDevices state=%q: %v", filter.State, err)
		}
		out := map[string]bool{}
		for _, d := range page.Items {
			out[d.ID] = true
		}
		return out
	}

	if got := ids(DeviceFilter{}); got[deadID] || !got[activeID] {
		t.Fatalf("default list must hide decommissioned, got %v", got)
	}
	if got := ids(DeviceFilter{State: StateAll}); !got[deadID] || !got[activeID] {
		t.Fatalf("ALL must include every state, got %v", got)
	}
	if got := ids(DeviceFilter{State: "DECOMMISSIONED"}); !got[deadID] || got[activeID] {
		t.Fatalf("DECOMMISSIONED filter must return only the tombstone, got %v", got)
	}
}
