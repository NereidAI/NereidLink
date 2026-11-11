// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gateway_db_test.go verifies gateway device creation, sub-device binding
// and the sub-device assertion used by the MQTT gateway presence flow
// (ADR-0010) against an opt-in PostgreSQL database.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const gatewayTestTenant = "00000000-0000-0000-0000-00000000d701"

// TestGatewaySubdeviceBinding exercises create/assert/deny of the gateway
// relationship.
func TestGatewaySubdeviceBinding(t *testing.T) {
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
		for _, id := range []string{
			"00000000-0000-0000-0000-00000000d711",
			"00000000-0000-0000-0000-00000000d710",
		} {
			_, _ = pool.Exec(ctx, `DELETE FROM device WHERE id = $1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, gatewayTestTenant)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'gateway-test')
		 ON CONFLICT (id) DO NOTHING`, gatewayTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM device_ownership_event WHERE device_id IN (
		    SELECT id FROM device
		    WHERE tenant_id = $1 AND canonical_name IN ('gw-1', 'sub-1'))`,
		gatewayTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM device
		 WHERE tenant_id = $1 AND canonical_name IN ('gw-1', 'sub-1')`,
		gatewayTestTenant); err != nil {
		t.Fatal(err)
	}

	gateway, err := admin.CreateDevice(ctx, gatewayTestTenant, DeviceDTO{
		CanonicalName: "gw-1",
		State:         StateActive,
		IsGateway:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := admin.CreateDevice(ctx, gatewayTestTenant, DeviceDTO{
		CanonicalName: "sub-1",
		State:         StateActive,
		GatewayID:     gateway.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	ok, err := admin.AssertGatewaySubdevice(ctx, gatewayTestTenant, gateway.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("bound sub-device was not accepted")
	}
	ok, err = admin.AssertGatewaySubdevice(ctx, gatewayTestTenant, gateway.ID, gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("gateway must not attach itself as a sub-device")
	}
	ok, err = admin.AssertGatewaySubdevice(ctx, gatewayTestTenant, sub.ID, gateway.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("sub-device must not be accepted as a gateway")
	}
}
