// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go verifies protocol_catalog create/get/list/update and the
// builtin delete protection against opt-in PostgreSQL (migration 0063). The
// table is global, so the fixture only needs to remove its own custom row.

package protocolcatalog

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newStore(t *testing.T) (*Store, *pgxpool.Pool) {
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

func TestStoreCRUDAndBuiltinGuard(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM protocol_catalog WHERE family='zz-test'`)
	})

	in := SaveInput{
		Family:      "zz-test",
		DisplayName: "ZZ Test",
		ServiceName: "device-zz",
		Transports:  []Transport{{Value: "zz", Label: "ZZ"}},
		ConnectionFields: []FieldSpec{
			{Key: "Host", Label: "Host", Kind: "string", Required: true},
		},
	}
	created, err := store.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Builtin {
		t.Fatal("operator-created family must not be builtin")
	}
	if len(created.Transports) != 1 || created.Transports[0].Value != "zz" {
		t.Fatalf("transports not persisted: %+v", created.Transports)
	}

	got, err := store.Get(ctx, "zz-test")
	if err != nil || got.DisplayName != "ZZ Test" {
		t.Fatalf("get: %v %+v", err, got)
	}

	updated, err := store.Update(ctx, "zz-test", SaveInput{
		Family: "zz-test", DisplayName: "ZZ Renamed"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Builtin || updated.DisplayName != "ZZ Renamed" {
		t.Fatalf("unexpected update result: %+v", updated)
	}

	list, err := store.List(ctx, "zz-test")
	if err != nil || len(list) != 1 {
		t.Fatalf("list filtered: len=%d err=%v", len(list), err)
	}

	if err := store.Delete(ctx, "zz-test"); err != nil {
		t.Fatalf("delete custom: %v", err)
	}
	if _, err := store.Get(ctx, "zz-test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}

	// A seeded builtin family is editable but cannot be deleted.
	if _, err := store.Get(ctx, "s7"); err != nil {
		t.Skipf("seed family s7 absent (migrations not applied): %v", err)
	}
	if err := store.Delete(ctx, "s7"); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("want ErrBuiltin deleting s7, got %v", err)
	}
}
