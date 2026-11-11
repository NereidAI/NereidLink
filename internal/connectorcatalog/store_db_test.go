// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go verifies connector_schema upsert/get/list and the
// exact-version then unversioned fallback lookup against opt-in PostgreSQL
// (migration 0043).

package connectorcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const catalogTestTenant = "00000000-0000-0000-0000-00000000d802"

func newCatalogStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), pool
}

func resetCatalogFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM connector_schema WHERE tenant_id=$1`, catalogTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM tenant WHERE id=$1`, catalogTestTenant); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCatalogUpsertGetList(t *testing.T) {
	store, pool := newCatalogStore(t)
	resetCatalogFixture(t, pool)
	t.Cleanup(func() { resetCatalogFixture(t, pool) })
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'catalog-test')
		 ON CONFLICT (id) DO NOTHING`, catalogTestTenant); err != nil {
		t.Fatal(err)
	}

	reg := Registration{ConnectorType: "modbus", ConnectorTypeVersion: "1",
		ConfigSchema: json.RawMessage(`{"type":"object","required":["host"]}`)}
	sch, err := store.Upsert(ctx, reg, catalogTestTenant, "", SourceEdge)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if sch.Source != SourceEdge || sch.ConnectorType != "modbus" {
		t.Fatalf("unexpected row: %+v", sch)
	}
	got, err := store.Get(ctx, catalogTestTenant, "modbus", "1")
	if err != nil || string(got.ConfigSchema) == "" {
		t.Fatalf("get exact: %v %+v", err, got)
	}

	// Re-register replaces the document (unique conflict -> upsert).
	reg.ConfigSchema = json.RawMessage(`{"type":"object"}`)
	if _, err := store.Upsert(ctx, reg, catalogTestTenant, "", SourceEdge); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM connector_schema
		 WHERE tenant_id=$1 AND connector_type='modbus'`, catalogTestTenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("re-registration must not duplicate, count=%d", n)
	}

	// Unversioned fallback: register "" and request a different version.
	unv := Registration{ConnectorType: "onvif",
		ConfigSchema: json.RawMessage(`{"type":"object"}`)}
	if _, err := store.Upsert(ctx, unv, catalogTestTenant, "", SourceEdge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, catalogTestTenant, "onvif", "7.7"); err != nil {
		t.Fatalf("must fall back to unversioned schema, got %v", err)
	}

	if _, err := store.Get(ctx, catalogTestTenant, "missing", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown type must be ErrNotFound, got %v", err)
	}

	list, err := store.List(ctx, catalogTestTenant)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}
}
