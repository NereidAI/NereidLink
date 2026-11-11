// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// device_counts_db_test.go guards the customer/group device-count queries
// against unassigned devices: a NULL customer_id forms its own GROUP BY group
// that could not be scanned into the string map key and previously 500'd the
// whole request. Unassigned devices must simply be excluded.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const deviceCountsTestTenant = "00000000-0000-0000-0000-00000000d731"

func resetDeviceCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, _ = pool.Exec(ctx, `DELETE FROM device_ownership_event WHERE tenant_id=$1`, deviceCountsTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device WHERE tenant_id=$1`, deviceCountsTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE tenant_id=$1`, deviceCountsTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM audit_event WHERE tenant_id=$1`, deviceCountsTestTenant)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'device-counts-test')
		 ON CONFLICT (id) DO NOTHING`, deviceCountsTestTenant); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCountsIgnoreUnassigned(t *testing.T) {
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

	resetDeviceCounts(t, ctx, pool)
	t.Cleanup(func() { resetDeviceCounts(t, ctx, pool) })

	var customerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO customer (id, tenant_id, name)
		 VALUES ($1,$2,'c1') RETURNING id::text`,
		uuid.NewString(), deviceCountsTestTenant).Scan(&customerID); err != nil {
		t.Fatal(err)
	}

	mkDevice := func(customer any) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
			 VALUES ($1,$2,$3,'ACTIVE',$4)`,
			uuid.NewString(), deviceCountsTestTenant,
			"dc-"+uuid.NewString()[:8], customer); err != nil {
			t.Fatal(err)
		}
	}
	// Two assigned to the customer, two unassigned (NULL customer_id).
	mkDevice(customerID)
	mkDevice(customerID)
	mkDevice(nil)
	mkDevice(nil)

	// Previously this returned an error scanning the NULL group into a string.
	byCustomer, err := admin.DeviceCountsByCustomer(ctx, deviceCountsTestTenant)
	if err != nil {
		t.Fatalf("DeviceCountsByCustomer must tolerate unassigned devices: %v", err)
	}
	if got := byCustomer[customerID]; got != 2 {
		t.Fatalf("customer count: want 2 assigned, got %d (%v)", got, byCustomer)
	}
	if len(byCustomer) != 1 {
		t.Fatalf("unassigned devices must not appear as a key, got %v", byCustomer)
	}

	// No device is assigned to a group: the group map must be empty, not error.
	byGroup, err := admin.DeviceCountsByGroup(ctx, deviceCountsTestTenant)
	if err != nil {
		t.Fatalf("DeviceCountsByGroup must tolerate unassigned devices: %v", err)
	}
	if len(byGroup) != 0 {
		t.Fatalf("group count map must be empty with no grouped devices, got %v", byGroup)
	}
}
