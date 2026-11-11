// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// query_test.go verifies the read side of the link episode ledger:
// ordering, source/gateway filters and the paged envelope. Integration
// test, gated on NEREIDLINK_TEST_DSN.

package linknotify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestListEpisodes inserts lease and edge episodes and asserts ordering
// and filtering through Store.ListEpisodes.
func TestListEpisodes(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping link episode query integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tenantID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'link-episode-query-test')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenantID) })

	gatewayID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES ($1, $2, 'lq-test-gateway', TRUE)`,
		gatewayID, tenantID); err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour)
	insert := func(id string, source string, start, end time.Time, alarm bool) {
		var alarmID any
		if alarm {
			alarmID = uuid.NewString()
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO gateway_link_episode
			   (id, tenant_id, gateway_id, started_at, ended_at,
			    down_reason, source, edge_down_at, alarm_id)
			 VALUES ($1,$2,$3,$4,$5,'test reason',$6,$7,$8)`,
			id, tenantID, gatewayID, start, end, source,
			nullableForInsert(source, start), alarmID); err != nil {
			t.Fatalf("insert episode %s: %v", id, err)
		}
	}
	leaseEnd := base.Add(10 * time.Minute)
	edgeStart := base.Add(30 * time.Minute)
	insert(uuid.NewString(), EpisodeSourceLease, base, leaseEnd, true)
	insert(uuid.NewString(), EpisodeSourceEdge, edgeStart, edgeStart.Add(5*time.Second), false)

	store := NewStore(pool)

	page, err := store.ListEpisodes(ctx, tenantID, EpisodeFilter{GatewayID: gatewayID}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("want 2 rows, got total=%d items=%d", page.Total, len(page.Items))
	}
	// Newest first.
	if page.Items[0].Source != EpisodeSourceEdge {
		t.Fatalf("newest row must be the edge episode, got source=%s", page.Items[0].Source)
	}
	// Edge micro-flap carries no alarm and its edge ground-truth time.
	first := page.Items[0]
	if first.AlarmID != "" || first.EdgeDownAt == "" || first.EndedAt == "" {
		t.Fatalf("edge row malformed: %+v", first)
	}

	// Source filter.
	edgeOnly, err := store.ListEpisodes(ctx, tenantID,
		EpisodeFilter{GatewayID: gatewayID, Source: EpisodeSourceEdge}, 1, 10)
	if err != nil {
		t.Fatalf("filter edge: %v", err)
	}
	if edgeOnly.Total != 1 || edgeOnly.Items[0].Source != EpisodeSourceEdge {
		t.Fatalf("source filter returned %+v", edgeOnly)
	}

	// Time window: a window ending before the edge start keeps only the lease row.
	windowed, err := store.ListEpisodes(ctx, tenantID, EpisodeFilter{
		GatewayID: gatewayID,
		Start:     base.Add(-time.Minute),
		End:       edgeStart,
	}, 1, 10)
	if err != nil {
		t.Fatalf("window query: %v", err)
	}
	if windowed.Total != 1 || windowed.Items[0].Source != EpisodeSourceLease {
		t.Fatalf("time window returned %+v", windowed)
	}
}

// nullableForInsert returns the edge_down_at value: same as start for edge
// rows, NULL for lease rows.
func nullableForInsert(source string, start time.Time) any {
	if source == EpisodeSourceEdge {
		return start
	}
	return nil
}
