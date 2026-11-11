// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReconcileViewFromWatermark exercises migration 0055 against a real
// database: StampManifest persists the gateway-level watermark, and the
// read-only ReconcileView classifies desired specs as in_sync/stale/missing
// plus reports extra edge ids, all without accepting a new manifest or
// mutating state.
func TestReconcileViewFromWatermark(t *testing.T) {
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
	svc := NewService(nil, store, nil, DefaultTuning(), nil)

	tenant := "00000000-0000-0000-0000-00000000d705"
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM connector_manifest_watermark WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM connector_instance WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name LIKE 'cv-gw-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, tenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'reconcile-view')
		 ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	var gw string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id,tenant_id,canonical_name,is_gateway)
		 VALUES (gen_random_uuid(),$1,'cv-gw-a',TRUE) RETURNING id::text`,
		tenant).Scan(&gw); err != nil {
		t.Fatalf("gateway: %v", err)
	}

	mk := func(id string, revision int64) Spec {
		return Spec{ID: id, TenantID: tenant, GatewayID: gw,
			ConnectorType: "modbus-tcp", Revision: revision}
	}
	sync := mk("11111111-1111-1111-1111-111111111111", 1)
	stale := mk("22222222-2222-2222-2222-222222222222", 3)
	missing := mk("33333333-3333-3333-3333-333333333333", 1)
	for _, sp := range []Spec{sync, stale, missing} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO connector_instance
			   (id,tenant_id,gateway_id,connector_type,revision)
			 VALUES ($1,$2,$3,$4,$5)`,
			sp.ID, tenant, gw, sp.ConnectorType, sp.Revision); err != nil {
			t.Fatalf("seed spec %s: %v", sp.ID, err)
		}
	}

	// Before any manifest: no watermark, specs are left unclassified.
	pre, err := svc.ReconcileView(ctx, tenant, gw)
	if err != nil {
		t.Fatalf("pre view: %v", err)
	}
	if pre.Watermark != nil {
		t.Fatal("expected nil watermark before first manifest")
	}
	if len(pre.Missing)+len(pre.InSync)+len(pre.Stale) != 0 {
		t.Fatalf("no manifest must leave specs unclassified, got %+v", pre)
	}
	if _, err := store.GetWatermark(ctx, tenant, gw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound watermark, got %v", err)
	}

	// Edge reports: sync at rev 1 (in sync), stale at rev 2 (< desired 3),
	// missing spec omitted, plus an unknown extra id.
	extra := "99999999-9999-9999-9999-999999999999"
	reported := []ReportedInstance{
		{SpecID: sync.ID, Revision: 1},
		{SpecID: stale.ID, Revision: 2},
		{SpecID: extra, Revision: 1},
	}
	if _, err := store.StampManifest(ctx, tenant, gw, reported, time.Now().UTC()); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	view, err := svc.ReconcileView(ctx, tenant, gw)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.Watermark == nil {
		t.Fatal("expected watermark after manifest")
	}
	if len(view.InSync) != 1 || view.InSync[0] != sync.ID {
		t.Fatalf("in_sync wrong: %v", view.InSync)
	}
	if len(view.Stale) != 1 || view.Stale[0] != stale.ID {
		t.Fatalf("stale wrong: %v", view.Stale)
	}
	if len(view.Missing) != 1 || view.Missing[0] != missing.ID {
		t.Fatalf("missing wrong: %v", view.Missing)
	}
	if len(view.Extra) != 1 || view.Extra[0] != extra {
		t.Fatalf("extra wrong: %v", view.Extra)
	}

	// The read path must not rewrite the watermark or any spec state.
	again, err := svc.ReconcileView(ctx, tenant, gw)
	if err != nil {
		t.Fatalf("second view: %v", err)
	}
	if again.Watermark.ObservedAt != view.Watermark.ObservedAt {
		t.Fatal("read-only ReconcileView must not move the watermark timestamp")
	}
}
