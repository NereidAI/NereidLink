// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIKeyMigrationUpAndDown(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping migration integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	schema := "apikey_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	up, err := migrationsFS.ReadFile("migrations/0092_user_api_key.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile("migrations/0092_user_api_key.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	setup := "SET search_path TO " + schema + "; " +
		"CREATE TABLE tenant (id UUID PRIMARY KEY); " +
		"CREATE TABLE app_user (id UUID PRIMARY KEY, tenant_id UUID REFERENCES tenant(id)); "
	if _, err := pool.Exec(ctx, setup+string(up)); err != nil {
		t.Fatalf("apply up migration: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass($1) IS NOT NULL", schema+".api_key").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("up migration did not create api_key")
	}
	if _, err := pool.Exec(ctx, "SET search_path TO "+schema+"; "+string(down)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass($1) IS NOT NULL", schema+".api_key").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("down migration did not drop api_key")
	}
}
