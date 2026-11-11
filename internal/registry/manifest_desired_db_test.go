// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// manifest_desired_db_test.go verifies ListGatewaySubdevices returns the
// desired reconciliation set: ACTIVE/UNCLAIMED devices bound to the gateway,
// excluding a quarantined device and another gateway's children.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const manifestDesiredTenant = "00000000-0000-0000-0000-00000000d702"

func TestListGatewaySubdevices(t *testing.T) {
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

	t.Cleanup(func() {
		cleanManifestDesired(ctx, pool)
	})
	// Clean leftovers from a previous interrupted run before inserting.
	cleanManifestDesired(ctx, pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'manifest-desired-test')
		 ON CONFLICT (id) DO NOTHING`, manifestDesiredTenant); err != nil {
		t.Fatal(err)
	}

	gw, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-gw", State: StateActive, IsGateway: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-other-gw", State: StateActive, IsGateway: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	act, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-active", State: StateActive, GatewayID: gw.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	uncl, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-unclaimed", State: StateUnclaimed, GatewayID: gw.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	quar, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-quarantined", State: StateActive, GatewayID: gw.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE device SET state = $1 WHERE id = $2`,
		StateQuarantined, quar.ID); err != nil {
		t.Fatal(err)
	}
	foreign, err := admin.CreateDevice(ctx, manifestDesiredTenant, DeviceDTO{
		CanonicalName: "md-foreign", State: StateActive, GatewayID: other.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := admin.ListGatewaySubdevices(ctx, manifestDesiredTenant, gw.ID)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{act.ID: true, uncl.ID: true}
	if len(got) != len(want) {
		t.Fatalf("expected %d desired, got %d (%v)", len(want), len(got), got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected desired device %s (foreign=%s)", id, foreign.ID)
		}
	}
}

// cleanManifestDesired removes the isolated tenant's test devices and the
// tenant row, tolerating ownership-event references via ON DELETE paths.
func cleanManifestDesired(ctx context.Context, pool *pgxpool.Pool) {
	_, _ = pool.Exec(ctx,
		`DELETE FROM device_ownership_event WHERE device_id IN (
		    SELECT id FROM device
		    WHERE tenant_id = $1 AND canonical_name LIKE 'md-%')`,
		manifestDesiredTenant)
	_, _ = pool.Exec(ctx,
		`DELETE FROM device WHERE tenant_id = $1 AND canonical_name LIKE 'md-%'`,
		manifestDesiredTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, manifestDesiredTenant)
}
