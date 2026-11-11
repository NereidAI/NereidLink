// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestListDevicesConnectionFilter pins the connection-state filter: it reads
// the latest ts_presence boundary per device and classifies absent rows as
// UNKNOWN, independently of the lifecycle state filter.
func TestListDevicesConnectionFilter(t *testing.T) {
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
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM ts_presence WHERE tenant_id=$1`, deviceTypeTestTenant)
		resetDeviceTypeFixture(t, context.Background(), pool)
	})

	insert := func(name string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state, created_at)
			 VALUES ($1,$2,$3,'ACTIVE',now())`,
			id, deviceTypeTestTenant, name); err != nil {
			t.Fatal(err)
		}
		return id
	}
	presenceAt := func(id, state string, at time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO ts_presence (time, tenant_id, device_id, state, reason, session_id, gateway_id)
			 VALUES ($1,$2,$3,$4,'','test-session',$2)`,
			at, deviceTypeTestTenant, id, state); err != nil {
			t.Fatal(err)
		}
	}

	onlineID := insert("conn-online")
	offlineID := insert("conn-offline")
	unknownID := insert("conn-unknown")
	base := time.Now().UTC()
	// onlineID carries an older OFFLINE boundary before its newest ONLINE,
	// proving only the latest row decides.
	presenceAt(onlineID, "OFFLINE", base.Add(-time.Minute))
	presenceAt(onlineID, "ONLINE", base)
	presenceAt(offlineID, "OFFLINE", base)

	ids := func(conn string) map[string]bool {
		t.Helper()
		page, err := admin.ListDevices(ctx, deviceTypeTestTenant,
			DeviceFilter{State: StateAll, Connection: conn}, 1, 50)
		if err != nil {
			t.Fatalf("ListDevices connection=%q: %v", conn, err)
		}
		out := map[string]bool{}
		for _, d := range page.Items {
			out[d.ID] = true
		}
		return out
	}

	if got := ids("online"); !got[onlineID] || got[offlineID] || got[unknownID] {
		t.Fatalf("online filter mismatch: %v", got)
	}
	if got := ids("offline"); !got[offlineID] || got[onlineID] || got[unknownID] {
		t.Fatalf("offline filter mismatch: %v", got)
	}
	if got := ids("unknown"); !got[unknownID] || got[onlineID] || got[offlineID] {
		t.Fatalf("unknown filter mismatch: %v", got)
	}
	if got := ids(""); len(got) != 3 {
		t.Fatalf("empty connection filter must return all three, got %d", len(got))
	}
}
