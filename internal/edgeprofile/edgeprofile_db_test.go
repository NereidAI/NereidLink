// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/registry"
)

const dbTestTenant = "00000000-0000-0000-0000-00000000e056"

// TestEdgeProfileCacheAndAutoIngest exercises migrations 0056+0061 against a
// real database: report upsert/liveness in the per-gateway cache, automatic
// ingestion into the tenant profile library on first sight, descriptor
// creation and cache->library linkage, and idempotent re-report (unchanged
// hash refreshes liveness without duplicating).
func TestEdgeProfileCacheAndAutoIngest(t *testing.T) {
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

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway_edge_profile WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device_profile WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'edgeprofile-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gw string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES (gen_random_uuid(), $1, 'ep-gw', TRUE) RETURNING id::text`,
		dbTestTenant).Scan(&gw); err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	cache := NewStore(pool)
	admin := registry.NewAdmin(pool)
	svc := NewService(cache, admin)

	profile := Profile{Name: "Modbus-Temp", DeviceResources: []Resource{
		{Name: "temperature", ValueType: "Float64", Units: "C"},
		{Name: "running", ValueType: "Bool"},
		{Name: "blob", ValueType: "Binary"},
	}}
	if err := svc.Ingest(ctx, dbTestTenant, gw, Frame{Profiles: []Profile{profile}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Per-gateway cache.
	list, err := cache.ListForGateway(ctx, dbTestTenant, gw)
	if err != nil || len(list) != 1 || list[0].ResourceCount != 3 {
		t.Fatalf("list: %+v err=%v", list, err)
	}

	// First sight auto-creates the tenant library profile and links the cache
	// row to it (no explicit adopt step any more).
	lib, err := admin.GetProfileByName(ctx, dbTestTenant, "Modbus-Temp")
	if err != nil {
		t.Fatalf("library profile: %v", err)
	}
	if list[0].AdoptedProfileID != lib.ID {
		t.Fatalf("cache must link to library profile %s, got %q", lib.ID, list[0].AdoptedProfileID)
	}
	if lib.Source != registry.SourceEdge || lib.Availability != registry.AvailabilityDiscovered {
		t.Fatalf("new library profile source/availability wrong: %+v", lib)
	}
	if len(lib.Descriptors) != 3 {
		t.Fatalf("want 3 descriptors, got %+v", lib.Descriptors)
	}
	byKey := map[string]registry.DescriptorDTO{}
	for _, d := range lib.Descriptors {
		byKey[d.MeasurementKey] = d
	}
	if byKey["temperature"].DataType != "float" || byKey["temperature"].Unit != "C" {
		t.Fatalf("temperature descriptor wrong: %+v", byKey["temperature"])
	}
	if byKey["running"].DataType != "bool" {
		t.Fatalf("running descriptor wrong: %+v", byKey["running"])
	}

	// Provenance row records the reporting gateway.
	var sources int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM profile_gateway_source
		 WHERE profile_id=$1 AND gateway_id=$2::uuid`, lib.ID, gw).Scan(&sources); err != nil || sources != 1 {
		t.Fatalf("profile_gateway_source: %d err=%v", sources, err)
	}

	// Default adopt is now an idempotent link/resolve onto the library object.
	res, err := svc.Adopt(ctx, dbTestTenant, AdoptRequest{GatewayID: gw, Name: "Modbus-Temp"})
	if err != nil || res.Created || res.Updated || res.ProfileID != lib.ID {
		t.Fatalf("default adopt should resolve existing library profile, got %+v err=%v", res, err)
	}

	// Re-report identical content stays linked and does not duplicate.
	if err := svc.Ingest(ctx, dbTestTenant, gw, Frame{Profiles: []Profile{profile}}); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	list2, _ := cache.ListForGateway(ctx, dbTestTenant, gw)
	if len(list2) != 1 || list2[0].AdoptedProfileID != lib.ID {
		t.Fatalf("re-report must preserve linkage: %+v", list2)
	}
	var profiles int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM device_profile WHERE tenant_id=$1 AND name='Modbus-Temp'`,
		dbTestTenant).Scan(&profiles); err != nil {
		t.Fatalf("count library profiles: %v", err)
	}
	if profiles != 1 {
		t.Fatalf("library profile must stay single, got %d", profiles)
	}
}
