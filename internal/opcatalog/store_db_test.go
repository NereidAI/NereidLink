// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go exercises the catalog store against a real database.
// It regresses the NOT NULL jsonb localization columns (a nil map encodes to
// NULL, rejecting hello registrations with SQLSTATE 23502) and the
// per-gateway full-set reconciliation (migration 0080).

package opcatalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStoreGatewayAdvertisements verifies per-gateway advertisements persist
// with '{}' localization defaults and that full-set replacement removes keys
// absent from the new set without touching another gateway.
func TestStoreGatewayAdvertisements(t *testing.T) {
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
	store := NewStore(pool)

	const (
		tenantID  = "00000000-0000-0000-0000-00000000d080"
		gatewayA  = "00000000-0000-0000-0000-00000000d081"
		gatewayB  = "00000000-0000-0000-0000-00000000d082"
		operation = "test.nil-localization"
	)
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway_operation_advertisement WHERE operation=$1`, operation)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE id = ANY($1)`, []string{gatewayA, gatewayB})
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, tenantID)
	}
	clean()
	t.Cleanup(clean)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'opcatalog-store-test')`, tenantID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{gatewayA, gatewayB} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state, is_gateway)
			 VALUES ($1,$2,$3,'ACTIVE',TRUE)`, id, tenantID, "gw-"+id[len(id)-4:]); err != nil {
			t.Fatal(err)
		}
	}

	desc := Descriptor{
		Operation:   operation,
		Interaction: InteractionRequestResponse,
		TimeoutMs:   1000,
		Risk:        RiskNormal,
	}
	if err := store.ReplaceGatewayAdvertisements(ctx, gatewayA,
		[]Descriptor{desc, {
			Operation: "test.gone", Interaction: InteractionRequestResponse, Risk: RiskRead,
			TimeoutMs: 1000,
		}}); err != nil {
		t.Fatalf("replace advertisements: %v", err)
	}
	// Gateway B advertises the same key but a different contract; it must
	// persist independently.
	other := desc
	other.TimeoutMs = 3000
	if err := store.ReplaceGatewayAdvertisements(ctx, gatewayB, []Descriptor{other}); err != nil {
		t.Fatalf("replace advertisements B: %v", err)
	}

	// Nil localization maps persist as '{}', not NULL.
	ads, err := store.ListTenantAdvertised(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, a := range ads {
		if a.Operation == operation {
			found++
			if a.DisplayName == nil {
				t.Fatal("display_name stored as NULL")
			}
		}
	}
	if found != 2 {
		t.Fatalf("expected both gateways advertising %s, got %d", operation, found)
	}

	// Selecting resolves that gateway's own descriptor.
	got, ok, err := store.SelectGatewayAdvertisement(ctx, tenantID, gatewayB, operation)
	if err != nil || !ok {
		t.Fatalf("select B: ok=%v err=%v", ok, err)
	}
	if got.TimeoutMs != 3000 {
		t.Fatalf("expected B timeout 3000, got %d", got.TimeoutMs)
	}

	// Full-set reconciliation removes a key absent from the next hello.
	if err := store.ReplaceGatewayAdvertisements(ctx, gatewayA, []Descriptor{desc}); err != nil {
		t.Fatalf("reconcile A: %v", err)
	}
	listA, err := store.ListGatewayAdvertisements(ctx, tenantID, gatewayA)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range listA {
		if d.Operation == "test.gone" {
			t.Fatal("removed operation should have been reconciled away")
		}
	}
}
