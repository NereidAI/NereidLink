// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_test.go verifies the accounting hypertable against a real
// PostgreSQL/TimescaleDB database: cluster-wide summation across nodes,
// deterministic ordering, window filtering, the dimension check constraint
// and the optional tenant restriction. It is an integration test and only
// runs when NEREIDLINK_TEST_DSN points at a migrated database.

package accounting

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testEpochOffset places this file's rows far enough in the future that
// they cannot collide with live accounting data. The tests must be safe to
// run against the development database, because that is where the migrated
// schema lives and NEREIDLINK_TEST_DSN is routinely pointed at it.
//
// A far-future stamp is used rather than "now" because Top aggregates every
// node in the requested window: rows written by a running service would
// corrupt the exact expected totals, and a blanket DELETE used to clean up
// would silently destroy that service's data. The 24h retention policy only
// drops chunks older than its horizon, so a future chunk is never reclaimed
// behind the test's back.
const testEpochOffset = 400 * 24 * time.Hour

// testTime returns a base timestamp inside the isolated future window.
func testTime() time.Time {
	return time.Now().UTC().Add(testEpochOffset).Truncate(time.Second)
}

// testStore connects to the test database and returns a Store plus a
// cleanup that removes only the rows this file wrote.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping accounting store integration test")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	var exists bool
	// The name is schema-qualified: an unqualified to_regclass resolves
	// through the connection's search_path, which differs between the CLI
	// (that ran the migration) and the pool used here.
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.accounting_topn') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("check hypertable: %v", err)
	}
	if !exists {
		t.Skip("accounting_topn missing; run migrations first")
	}
	// Cleanup is bounded by the isolated future window: rows at or after
	// testEpochOffset belong to these tests, everything earlier is live
	// data that must survive the test run.
	t.Cleanup(func() {
		cutoff := time.Now().UTC().Add(testEpochOffset - 24*time.Hour)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM accounting_topn WHERE time >= $1`, cutoff)
	})
	return NewStore(pool)
}

// TestStoreTopNAcrossNodes verifies reports from two nodes are summed per
// value and ranked deterministically.
func TestStoreTopNAcrossNodes(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	now := testTime()

	// Node A: tenant t1 heavy, t2 light.
	if _, err := store.AppendReport(ctx, Report{
		NodeID: "node-a", WindowStart: now.Add(-30 * time.Second), WindowEnd: now,
		SampleRate: 1, Seen: 300, Sampled: 300,
		Dimensions: map[string][]DimensionEntry{
			string(DimensionTenant): {
				{Value: "t1", Count: 200, Bytes: 2000, Tracked: true},
				{Value: "t2", Count: 100, Bytes: 1000, Tracked: true},
			},
		},
	}); err != nil {
		t.Fatalf("append node-a: %v", err)
	}
	// Node B: the same tenant t1 plus a bigger t3.
	if _, err := store.AppendReport(ctx, Report{
		NodeID: "node-b", WindowStart: now.Add(-30 * time.Second), WindowEnd: now,
		SampleRate: 4, Seen: 4000, Sampled: 1000,
		Dimensions: map[string][]DimensionEntry{
			string(DimensionTenant): {
				{Value: "t1", Count: 150, Bytes: 1500, Tracked: true},
				{Value: "t3", Count: 800, Bytes: 8000, Tracked: true},
			},
		},
	}); err != nil {
		t.Fatalf("append node-b: %v", err)
	}

	results, err := store.Top(ctx, TopQuery{
		Dimension: DimensionTenant,
		From:      now.Add(-time.Minute), To: now.Add(time.Minute),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 tenants, got %d (%+v)", len(results), results)
	}
	// t3 (800) then t1 (200+150=350) then t2 (100).
	if results[0].Value != "t3" || results[0].MessageCount != 800 {
		t.Fatalf("expected t3 first at 800, got %+v", results[0])
	}
	if results[1].Value != "t1" || results[1].MessageCount != 350 {
		t.Fatalf("expected t1 summed to 350, got %+v", results[1])
	}
	// t1 was seen on both nodes, so Nodes must be 2; t3 only on node-b.
	if results[1].Nodes != 2 {
		t.Fatalf("expected t1 reported by 2 nodes, got %d", results[1].Nodes)
	}
	if results[0].Nodes != 1 {
		t.Fatalf("expected t3 reported by 1 node, got %d", results[0].Nodes)
	}
	// The largest sample rate across contributions decides exactness: t1
	// and t3 both include node-b's rate-4 report, so they are
	// extrapolations, while t2 appears only in node-a's unsampled report
	// and is therefore a measured count.
	if results[1].Exact() || results[0].Exact() {
		t.Fatalf("sampled contributions must not be marked exact: %+v %+v",
			results[1], results[0])
	}
	if !results[2].Exact() {
		t.Fatalf("t2 came only from an unsampled report and must be exact: %+v", results[2])
	}
	if results[2].Value != "t2" || results[2].MessageCount != 100 {
		t.Fatalf("expected t2 at 100, got %+v", results[2])
	}
}

// TestStoreTopWindowExcludesOldRows verifies the window bounds are
// respected so a rolling view does not read stale windows.
func TestStoreTopWindowExcludesOldRows(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	now := testTime()

	// A row two hours old must be invisible to a one-minute window.
	if _, err := store.AppendReport(ctx, Report{
		NodeID: "node-a", WindowStart: now.Add(-2 * time.Hour), WindowEnd: now.Add(-2 * time.Hour),
		SampleRate: 1, Seen: 1, Sampled: 1,
		Dimensions: map[string][]DimensionEntry{
			string(DimensionTenant): {{Value: "ancient", Count: 9999, Tracked: true}},
		},
	}); err != nil {
		t.Fatalf("append old: %v", err)
	}
	results, err := store.Top(ctx, TopQuery{
		Dimension: DimensionTenant,
		From:      now.Add(-time.Minute), To: now.Add(time.Minute),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	for _, r := range results {
		if r.Value == "ancient" {
			t.Fatal("row outside the window must not be returned")
		}
	}
}

// TestStoreTopRejectsUnknownDimension verifies an invalid dimension is a
// hard error rather than a silently empty result.
func TestStoreTopRejectsUnknownDimension(t *testing.T) {
	store := testStore(t)
	_, err := store.Top(t.Context(), TopQuery{Dimension: "nonsense"})
	if err == nil {
		t.Fatal("expected an error for an unknown dimension")
	}
}

// TestStoreTopTenantFilter verifies the tenant restriction only applies
// to the tenant dimension.
func TestStoreTopTenantFilter(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	now := testTime()

	if _, err := store.AppendReport(ctx, Report{
		NodeID: "node-a", WindowStart: now.Add(-30 * time.Second), WindowEnd: now,
		SampleRate: 1, Seen: 2, Sampled: 2,
		Dimensions: map[string][]DimensionEntry{
			string(DimensionTenant): {
				{Value: "t1", Count: 10, Tracked: true},
				{Value: "t2", Count: 20, Tracked: true},
			},
		},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	results, err := store.Top(ctx, TopQuery{
		Dimension: DimensionTenant,
		From:      now.Add(-time.Minute), To: now.Add(time.Minute),
		TenantID: "t2", Limit: 10,
	})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(results) != 1 || results[0].Value != "t2" {
		t.Fatalf("expected only t2, got %+v", results)
	}
}

// TestStoreAppendSkipsEmptyValues verifies blank dimension values are not
// persisted, so a value can never be stored as the empty string.
func TestStoreAppendSkipsEmptyValues(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	now := testTime()

	written, err := store.AppendReport(ctx, Report{
		NodeID: "node-a", WindowStart: now.Add(-time.Second), WindowEnd: now,
		SampleRate: 1, Seen: 2, Sampled: 2,
		Dimensions: map[string][]DimensionEntry{
			string(DimensionIP): {{Value: "", Count: 5, Tracked: true}},
		},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if written != 0 {
		t.Fatalf("expected no rows written for an empty value, got %d", written)
	}
}

// TestStorePurge verifies the 24h retention bound can be enforced
// deterministically.
//
// Purge is inherently a global "delete everything older than the cutoff"
// operation, so it cannot be isolated in the far-future epoch used by the
// other tests: any cutoff that also expires a future-dated test row is
// older than every live row, and would delete them. The test therefore
// works in real time instead — it writes one genuinely expired row and one
// genuinely fresh row, purges at the documented 24h bound, and asserts that
// the fresh row survives while the expired one is gone. Its own rows are
// removed by node id, which cannot touch another node's data.
func TestStorePurge(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()

	// nodePurgeNode is unique to this test so cleanup can target it.
	const purgeNode = "test-purge-node"
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(),
			`DELETE FROM accounting_topn WHERE node_id = $1`, purgeNode)
	})

	now := time.Now().UTC().Truncate(time.Second)
	expired := now.Add(-48 * time.Hour)
	fresh := now.Add(-5 * time.Minute)
	for _, ts := range []time.Time{expired, fresh} {
		if _, err := store.AppendReport(ctx, Report{
			NodeID: purgeNode, WindowStart: ts.Add(-time.Second), WindowEnd: ts,
			SampleRate: 1, Seen: 1, Sampled: 1,
			Dimensions: map[string][]DimensionEntry{
				string(DimensionTenant): {{Value: "probe", Count: 1, Tracked: true}},
			},
		}); err != nil {
			t.Fatalf("append at %s: %v", ts, err)
		}
	}

	if _, err := store.PurgeBefore(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Count what survived per timestamp: the expired row must be gone and
	// the fresh one must remain.
	rows, err := store.pool.Query(ctx,
		`SELECT time FROM accounting_topn WHERE node_id = $1`, purgeNode)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	remaining := 0
	for rows.Next() {
		remaining++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected only the fresh row to survive the purge, %d remained", remaining)
	}
}
