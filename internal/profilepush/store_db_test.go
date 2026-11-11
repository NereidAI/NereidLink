// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go exercises the real SQL of the profile-delivery store
// against PostgreSQL. It is skipped unless NL_TEST_DATABASE_URL is set, and it
// expects the 0085 migration plus the device/device_profile/descriptor schema
// it references to already exist (the same convention the other *_db_test.go
// files follow). The value is that it runs the actual queries — in particular
// the UPDATE ... FROM ... RETURNING claim statements, which are easy to get
// wrong — rather than a stub.

package profilepush

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// claimsFor filters a claim result to rows belonging to one gateway, so the
// assertions ignore rows other test runs left in the shared database.
func claimsFor(in []Delivery, gatewayID string) []Delivery {
	out := []Delivery{}
	for _, d := range in {
		if d.GatewayID == gatewayID {
			out = append(out, d)
		}
	}
	return out
}

// dbFixture connects, creates a throwaway tenant and returns the store plus
// seeded ids.
func dbFixture(t *testing.T) (*Store, *pgxpool.Pool, string, string, string) {
	t.Helper()
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

	tenant := uuid.NewString()
	gateway := uuid.NewString()
	profile := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenant (id) VALUES ($1)`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES ($1,$2,'gw-b',TRUE)`, gateway, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name, protocol, edge_payload)
		 VALUES ($1,$2,'A1','modbus',
		   '{"name":"A1","protocol":"modbus","description":"Boiler",
		     "points":[{"name":"temperature","type":"float","unit":"Cel",
		                "read_write":"R","min":-40,"max":150,
		                "address":{"slave_id":1,"offset":10}}]}'::jsonb)`,
		profile, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO measurement_descriptor
		   (tenant_id, profile_id, measurement_key, data_type, unit, read_write,
		    address, edge_min, edge_max)
		 VALUES ($1,$2,'temperature','float','Cel','R',
		   '{"slave_id":1,"offset":10}'::jsonb,-40,150)`,
		tenant, profile); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool), pool, tenant, gateway, profile
}

// TestStoreReconcileClaimAndReport walks the store SQL end to end.
func TestStoreReconcileClaimAndReport(t *testing.T) {
	store, _, tenant, gateway, profile := dbFixture(t)
	ctx := context.Background()

	// Library read recovers payload description and descriptor points.
	lib, err := store.LibraryProfile(ctx, tenant, profile)
	if err != nil {
		t.Fatalf("library profile: %v", err)
	}
	if lib.Description != "Boiler" || len(lib.Points) != 1 {
		t.Fatalf("unexpected library profile: %+v", lib)
	}
	if lib.Points[0].Min == nil || *lib.Points[0].Min != -40 {
		t.Fatalf("range not read: %+v", lib.Points[0])
	}

	// Revision bumps only when the hash changes.
	rev1, err := store.BumpRevision(ctx, tenant, profile, "hash-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("bump 1: %v", err)
	}
	rev1b, err := store.BumpRevision(ctx, tenant, profile, "hash-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("bump 1 repeat: %v", err)
	}
	if rev1 != rev1b {
		t.Fatalf("hash unchanged must not bump: %d != %d", rev1, rev1b)
	}
	rev2, err := store.BumpRevision(ctx, tenant, profile, "hash-2", time.Now().UTC())
	if err != nil {
		t.Fatalf("bump 2: %v", err)
	}
	if rev2 != rev1+1 {
		t.Fatalf("hash change must bump: %d -> %d", rev1, rev2)
	}

	// Desired sync, then dispatch marking, then a failed report that the
	// claim sweep must pick up.
	if err := store.SyncDesired(ctx, tenant, profile, rev2, []string{gateway}, time.Now().UTC()); err != nil {
		t.Fatalf("sync desired: %v", err)
	}
	if err := store.MarkDispatched(ctx, tenant, profile, gateway, rev2, time.Now().UTC()); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	if err := store.Report(ctx, tenant, gateway, StatusEntry{
		Name: "A1", Revision: rev2, Status: StateFailed, Reason: "boom",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("report: %v", err)
	}
	claimed, err := store.ClaimFailedForRaise(ctx)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	mine := claimsFor(claimed, gateway)
	if len(mine) != 1 || mine[0].Reason != "boom" {
		t.Fatalf("expected one failed claim, got %+v", mine)
	}
	if mine[0].TenantID != tenant || mine[0].ProfileName != "A1" {
		t.Fatalf("claim projection wrong: %+v", mine[0])
	}

	// An APPLIED report clears the failed state; a later escalation claim is
	// then empty.
	if err := store.Report(ctx, tenant, gateway, StatusEntry{
		Name: "A1", Revision: rev2, Status: StateApplied,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	esc, err := store.ClaimFailedForEscalation(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("claim escalation: %v", err)
	}
	if len(claimsFor(esc, gateway)) != 0 {
		t.Fatalf("applied delivery must not escalate: %+v", esc)
	}

	// The pending sweep query must also execute (no rows expected here).
	if _, err := store.ClaimPendingForAlarm(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("claim pending: %v", err)
	}

	// Removal and gateway-scoped listing.
	if err := store.MarkRemoving(ctx, tenant, profile, gateway, time.Now().UTC()); err != nil {
		t.Fatalf("mark removing: %v", err)
	}
	rows, err := store.DeliveriesByGateway(ctx, tenant, gateway)
	if err != nil {
		t.Fatalf("deliveries by gateway: %v", err)
	}
	if len(rows) != 1 || rows[0].State != StateRemoving {
		t.Fatalf("expected REMOVING row, got %+v", rows)
	}

	// Target add/remove is idempotent.
	if err := store.AddTarget(ctx, tenant, profile, gateway, time.Now().UTC()); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if err := store.AddTarget(ctx, tenant, profile, gateway, time.Now().UTC()); err != nil {
		t.Fatalf("add target repeat: %v", err)
	}
	targets, err := store.ListTargets(ctx, tenant, profile)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 || targets[0].GatewayName != "gw-b" {
		t.Fatalf("expected one named target, got %+v", targets)
	}

	// Implicit reference set via connector_instance.
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO connector_instance (tenant_id, gateway_id, profile_id)
		 VALUES ($1,$2,$3)`, tenant, gateway, profile); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	refs, err := store.ReferencedProfiles(ctx, tenant, gateway)
	if err != nil {
		t.Fatalf("referenced profiles: %v", err)
	}
	if len(refs) != 1 || refs[0] != profile {
		t.Fatalf("expected connector reference, got %+v", refs)
	}

	// Gateway formats stamp round trip.
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='device' AND column_name='agent_profile_formats'`).Scan(new(int)); err != nil {
		t.Fatalf("formats column missing: %v", err)
	}
}
