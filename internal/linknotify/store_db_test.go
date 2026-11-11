// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package linknotify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTestTenant = "00000000-0000-0000-0000-00000000d703"

// TestStoreEpisodeLifecycleAndDigest exercises the migration-0039 tables
// against a real database: open dedupe, due-claim escalation, close and the
// daily digest aggregation/once-only cursor.
func TestStoreEpisodeLifecycleAndDigest(t *testing.T) {
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

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway_link_episode WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM gateway_link_digest WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name IN ('ln-gw-a','ln-gw-b')`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'link-notify-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	mkGateway := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
			 VALUES (gen_random_uuid(), $1, $2, TRUE) RETURNING id::text`,
			dbTestTenant, name).Scan(&id); err != nil {
			t.Fatalf("create gateway %s: %v", name, err)
		}
		return id
	}
	gwA := mkGateway("ln-gw-a")
	gwB := mkGateway("ln-gw-b")

	base := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)

	// gwA opens a 40-minute outage; it is still open at the +30m escalation
	// claim, then recovers. gwB later has two short outages the same UTC
	// day, both feeding the digest.
	if err := store.OpenEpisode(ctx, Episode{
		TenantID: dbTestTenant, GatewayID: gwA,
		StartedAt: base, Reason: "lease_expired", AlarmID: "00000000-0000-0000-0000-0000000000a1",
	}); err != nil {
		t.Fatal(err)
	}
	// Duplicate open for the same gateway must be suppressed.
	if err := store.OpenEpisode(ctx, Episode{
		TenantID: dbTestTenant, GatewayID: gwA,
		StartedAt: base, Reason: "transport_down", AlarmID: "00000000-0000-0000-0000-0000000000a2",
	}); err != nil {
		t.Fatal(err)
	}

	// At +30m the open gwA episode is due exactly; claim is at-most-once.
	now := base.Add(30 * time.Minute)
	due, err := store.ClaimDueEpisodes(ctx, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].GatewayID != gwA {
		t.Fatalf("expected only gwA due at +30m, got %+v", due)
	}
	due2, err := store.ClaimDueEpisodes(ctx, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due2) != 0 {
		t.Fatalf("claim must be at-most-once, second call got %d", len(due2))
	}

	// gwA recovers after 40 minutes (escalation already marked).
	if err := store.CloseEpisode(ctx, gwA, base.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// gwB: two short outages ending within the same UTC day.
	if err := store.OpenEpisode(ctx, Episode{
		TenantID: dbTestTenant, GatewayID: gwB,
		StartedAt: base.Add(2 * time.Hour), Reason: "transport_down",
		AlarmID: "00000000-0000-0000-0000-0000000000b1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseEpisode(ctx, gwB, base.Add(2*time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenEpisode(ctx, Episode{
		TenantID: dbTestTenant, GatewayID: gwB,
		StartedAt: base.Add(4 * time.Hour), Reason: "lease_expired",
		AlarmID: "00000000-0000-0000-0000-0000000000b2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseEpisode(ctx, gwB, base.Add(4*time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	tenants, err := store.TenantsWithEndedEpisodes(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range tenants {
		if id == dbTestTenant {
			found = true
		}
	}
	if !found {
		t.Fatal("tenant with ended episodes must be listed")
	}

	rows, err := store.DigestRows(ctx, dbTestTenant, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two gateway digest rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].GatewayID != gwA || rows[0].Count != 1 ||
		rows[0].TotalDowntime != 40*time.Minute {
		t.Fatalf("unexpected top row: %+v", rows[0])
	}
	var bCount int
	for _, r := range rows {
		if r.GatewayID == gwB {
			bCount = r.Count
		}
	}
	if bCount != 2 {
		t.Fatalf("gwB expected 2 episodes, got %d", bCount)
	}

	// Digest cursor is once per UTC day per tenant.
	sent, err := store.MarkDigestSent(ctx, dbTestTenant, day, time.Now().UTC())
	if err != nil || !sent {
		t.Fatalf("first mark must succeed, got %v %v", sent, err)
	}
	sent2, err := store.MarkDigestSent(ctx, dbTestTenant, day, time.Now().UTC())
	if err != nil || sent2 {
		t.Fatalf("second mark must report not-sent, got %v %v", sent2, err)
	}
}

// TestApplyEdgeEpisode exercises the edge episode path against a real
// database: a lease episode is corrected with the edge ground truth, the
// same report is then a duplicate, and an episode with no matching lease row
// is inserted as a closed alarm-free episode.
func TestApplyEdgeEpisode(t *testing.T) {
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

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway_link_episode WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name='ln-gw-edge'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'link-edge-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gw string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES (gen_random_uuid(), $1, 'ln-gw-edge', TRUE) RETURNING id::text`,
		dbTestTenant).Scan(&gw); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)

	// Lease detects the outage about 80s late: an open episode with guessed
	// start, alarm raised.
	if err := store.OpenEpisode(ctx, Episode{
		TenantID: dbTestTenant, GatewayID: gw,
		StartedAt: base.Add(80 * time.Second), Reason: "lease_expired",
		AlarmID: "00000000-0000-0000-0000-0000000000c1",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := store.ApplyEdgeEpisode(ctx, EdgeEpisode{
		TenantID: dbTestTenant, GatewayID: gw,
		DownAt: base, UpAt: base.Add(5 * time.Second),
		Reason: "transport_down",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ApplyAccepted || !res.MatchedLease {
		t.Fatalf("expected accepted/matched lease correction, got %+v", res)
	}

	var startedAt, endedAt, edgeDownAt, source string
	var alarmID *string
	if err := pool.QueryRow(ctx,
		`SELECT started_at::text, ended_at::text, source,
		        edge_down_at::text, alarm_id::text
		 FROM gateway_link_episode WHERE gateway_id=$1 AND source='edge'`,
		gw).Scan(&startedAt, &endedAt, &source, &edgeDownAt, &alarmID); err != nil {
		t.Fatal(err)
	}
	if startedAt != base.Format("2006-01-02 15:04:05+00") ||
		endedAt != base.Add(5*time.Second).Format("2006-01-02 15:04:05+00") {
		t.Fatalf("episode boundaries not corrected to edge truth: %s..%s", startedAt, endedAt)
	}
	if edgeDownAt != base.Format("2006-01-02 15:04:05+00") {
		t.Fatalf("edge_down_at not recorded: %s", edgeDownAt)
	}
	if alarmID != nil {
		t.Fatal("corrected row must keep its existing alarm for recovery clear")
	}

	// The same outage replayed (retransmission / backlog) is a duplicate.
	res2, err := store.ApplyEdgeEpisode(ctx, EdgeEpisode{
		TenantID: dbTestTenant, GatewayID: gw,
		DownAt: base, UpAt: base.Add(5 * time.Second),
		Reason: "transport_down",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != ApplyDuplicate {
		t.Fatalf("expected duplicate, got %+v", res2)
	}

	// A later micro-flap the lease never sees inserts a closed alarm-free row.
	down2 := base.Add(2 * time.Hour)
	res3, err := store.ApplyEdgeEpisode(ctx, EdgeEpisode{
		TenantID: dbTestTenant, GatewayID: gw,
		DownAt: down2, UpAt: down2.Add(3 * time.Second),
		Reason: "transport_down",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Status != ApplyAccepted || res3.MatchedLease {
		t.Fatalf("expected accepted/unmatched edge-only episode, got %+v", res3)
	}
	var count int
	var nullAlarm *string
	if err := pool.QueryRow(ctx,
		`SELECT count(*), alarm_id FROM gateway_link_episode
		 WHERE gateway_id=$1 AND edge_down_at=$2`,
		gw, down2).Scan(&count, &nullAlarm); err != nil {
		t.Fatal(err)
	}
	if count != 1 || nullAlarm != nil {
		t.Fatalf("edge-only episode must be one closed alarm-free row, count=%d alarm=%v", count, nullAlarm)
	}
}
