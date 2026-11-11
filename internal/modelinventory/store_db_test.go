// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go exercises the real SQL of the model-inventory store
// (migration 0086) against PostgreSQL. It is skipped unless
// NL_TEST_DATABASE_URL is set and the 0086 schema (and the device/tenant/
// device_profile tables it references) already exists, following the
// convention of the other *_db_test.go files. It covers the transactional
// full-replacement upsert/prune, the durable prune audit rows, the atomic
// at-most-once FAILED claim, lease staleness and the system-profile seed.

package modelinventory

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dbPool(t *testing.T) (*PGStore, *pgxpool.Pool, string, string) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO tenant (id, name) VALUES ($1,$2)`,
		tenant, "test-"+tenant[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES ($1,$2,$3,TRUE)`,
		gateway, tenant, "gw-"+gateway[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id=$1`, tenant)
	})
	return NewStore(pool), pool, tenant, gateway
}

func input(id, version, state string) Input {
	return Input{ID: id, Version: version, Task: "detection", Backend: "kserve",
		State: state, Labels: []string{"person"}}
}

func TestDBReconcileUpsertPruneClaimStale(t *testing.T) {
	store, pool, tenant, gateway := dbPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Initial full list: one READY, one FAILED.
	res, err := store.Reconcile(ctx, tenant, gateway, []Input{
		input("m1", "1.0.0", StateReady),
		input("m2", "1.0.0", StateFailed),
	}, now)
	if err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if len(res.Upserted) != 2 {
		t.Fatalf("want 2 rows, got %d", len(res.Upserted))
	}

	got, err := store.ListByGateway(ctx, tenant, gateway)
	if err != nil || len(got) != 2 {
		t.Fatalf("list: %v %+v", err, got)
	}
	for _, m := range got {
		if m.Stale || len(m.Labels) != 1 || m.Labels[0] != "person" {
			t.Fatalf("unexpected row: %+v", m)
		}
	}

	// FAILED claim is atomic and one-shot.
	claimed, err := store.ClaimFailedForRaise(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var mine []Model
	for _, c := range claimed {
		if c.GatewayID == gateway && c.ModelID == "m2" {
			mine = append(mine, c)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("want one claimed m2, got %d", len(mine))
	}
	claimed2, _ := store.ClaimFailedForRaise(ctx)
	for _, c := range claimed2 {
		if c.GatewayID == gateway {
			t.Fatalf("second claim must not return already-flagged rows: %+v", c)
		}
	}

	// Full replacement dropping m2 prunes it, writes one audit row and
	// reports the FAILED model for alarm clear.
	res2, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{input("m1", "1.0.0", StateReady)}, now)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if len(res2.Pruned) != 1 || res2.Pruned[0].ModelID != "m2" {
		t.Fatalf("want m2 pruned, got %+v", res2.Pruned)
	}
	if len(res2.ClearedFailed) != 1 || res2.ClearedFailed[0].ModelID != "m2" {
		t.Fatalf("want m2 failed-alarm clear, got %+v", res2.ClearedFailed)
	}
	var events int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM gateway_model_inventory_event
		 WHERE tenant_id=$1 AND model_id='m2' AND event='pruned'`,
		tenant).Scan(&events); err != nil || events != 1 {
		t.Fatalf("prune audit events=%d err=%v", events, err)
	}

	// Lease staleness flips once, then a successful models report recovers.
	newly, err := store.MarkStale(ctx, tenant, gateway, now)
	if err != nil || !newly {
		t.Fatalf("first MarkStale newly=%v err=%v", newly, err)
	}
	newly2, _ := store.MarkStale(ctx, tenant, gateway, now)
	if newly2 {
		t.Fatal("second MarkStale must report no new rows")
	}
	staleRows, _ := store.ListByGateway(ctx, tenant, gateway)
	if !staleRows[0].Stale || staleRows[0].StaleAt == nil {
		t.Fatalf("row should be stale: %+v", staleRows[0])
	}
	res3, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{input("m1", "1.0.0", StateReady)}, now)
	if err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	if !res3.WasStale {
		t.Fatal("recovery reconcile must report WasStale")
	}
	recovered, _ := store.ListByGateway(ctx, tenant, gateway)
	if recovered[0].Stale {
		t.Fatalf("row should be recovered: %+v", recovered[0])
	}

	// Explicit empty list prunes everything.
	res4, err := store.Reconcile(ctx, tenant, gateway, []Input{}, now)
	if err != nil {
		t.Fatalf("reconcile empty: %v", err)
	}
	if len(res4.Pruned) != 1 {
		t.Fatalf("want m1 pruned on empty list, got %+v", res4.Pruned)
	}
	empty, _ := store.ListByGateway(ctx, tenant, gateway)
	if len(empty) != 0 {
		t.Fatalf("empty gateway must return empty slice, got %d", len(empty))
	}
}

func TestDBReconcileServerFamily(t *testing.T) {
	store, _, tenant, gateway := dbPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	withServer := input("m1", "1.0.0", StateReady)
	withServer.ServerFamily = "ovms"
	if _, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{withServer}, now); err != nil {
		t.Fatalf("reconcile with server_family: %v", err)
	}
	got, err := store.ListByGateway(ctx, tenant, gateway)
	if err != nil || len(got) != 1 || got[0].ServerFamily != "ovms" {
		t.Fatalf("server_family readback: %v %+v", err, got)
	}

	// Full replacement: a later report without the token resets it to ''.
	if _, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{input("m1", "1.0.0", StateReady)}, now); err != nil {
		t.Fatalf("reconcile without server_family: %v", err)
	}
	reset, err := store.ListByGateway(ctx, tenant, gateway)
	if err != nil || len(reset) != 1 || reset[0].ServerFamily != "" {
		t.Fatalf("server_family must reset on full replacement: %v %+v", err, reset)
	}
}

func TestDBSegmentWatermarkStaleness(t *testing.T) {
	store, _, tenant, gateway := dbPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{input("m1", "1.0.0", StateReady)}, now); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Fresh watermark: nothing overdue, rows stay non-stale.
	fresh, err := store.MarkInventoryStale(ctx, now.Add(-DefaultInventoryStaleThreshold), now)
	if err != nil {
		t.Fatalf("fresh mark: %v", err)
	}
	if len(fresh) != 0 {
		t.Fatalf("fresh watermark must not flip stale, got %+v", fresh)
	}

	// Simulate the edge omitting the models segment long enough while
	// other manifest segments keep arriving: the watermark ages out.
	stale, err := store.MarkInventoryStale(ctx, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("overdue mark: %v", err)
	}
	if len(stale) != 1 || stale[0].GatewayID != gateway || stale[0].TenantID != tenant {
		t.Fatalf("want one flipped gateway, got %+v", stale)
	}
	rows, _ := store.ListByGateway(ctx, tenant, gateway)
	if len(rows) != 1 || !rows[0].Stale || rows[0].StaleAt == nil {
		t.Fatalf("row should be stale: %+v", rows)
	}
	// Idempotent across replicas/sweeps: already-stale rows flip no more.
	again, err := store.MarkInventoryStale(ctx, now.Add(time.Minute), now)
	if err != nil || len(again) != 0 {
		t.Fatalf("second mark must be inert, got %+v err=%v", again, err)
	}

	// A present segment recovers the row on the next reconcile.
	res, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{input("m1", "1.0.0", StateReady)}, now.Add(time.Hour))
	if err != nil || !res.WasStale {
		t.Fatalf("recovery reconcile WasStale=%v err=%v", res.WasStale, err)
	}
	recovered, _ := store.ListByGateway(ctx, tenant, gateway)
	if recovered[0].Stale {
		t.Fatalf("row should be recovered: %+v", recovered[0])
	}
}

func TestDBReconcileRejectsInvalid(t *testing.T) {
	store, _, tenant, gateway := dbPool(t)
	ctx := context.Background()
	if _, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{{ID: "", Version: "1", State: StateReady}}, time.Now()); err == nil {
		t.Fatal("missing model id must be rejected")
	}
	if _, err := store.Reconcile(ctx, tenant, gateway,
		[]Input{{ID: "x", Version: "1", State: "BOGUS"}}, time.Now()); err == nil {
		t.Fatal("unknown state must be rejected")
	}
}

func TestDBEnsureSystemProfile(t *testing.T) {
	store, pool, tenant, _ := dbPool(t)
	ctx := context.Background()

	if err := store.EnsureSystemProfile(ctx, tenant); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Deterministic id matches the SQL migration convention.
	wantID := SystemProfileID(tenant)
	var gotID, name string
	var isSystem bool
	if err := pool.QueryRow(ctx,
		`SELECT id::text, name, is_system FROM device_profile
		 WHERE tenant_id=$1 AND name='detection/v1'`, tenant).
		Scan(&gotID, &name, &isSystem); err != nil {
		t.Fatalf("load system profile: %v", err)
	}
	if gotID != wantID || !isSystem {
		t.Fatalf("system profile id=%s want=%s system=%v", gotID, wantID, isSystem)
	}
	var points int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM measurement_descriptor
		 WHERE profile_id=$1 AND data_type IS NOT NULL`, wantID).
		Scan(&points); err != nil {
		t.Fatalf("count descriptors: %v", err)
	}
	if points != 5 {
		t.Fatalf("want 5 detection/v1 descriptors, got %d", points)
	}
	// Idempotent: a second seed neither errors nor duplicates.
	if err := store.EnsureSystemProfile(ctx, tenant); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
}
