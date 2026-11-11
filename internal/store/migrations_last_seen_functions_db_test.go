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

// TestLastSeenFunctionsMigrationUpAndDown applies the 0093 pair inside an
// isolated schema. The plpgsql bodies reach the hypertables through a dynamic
// EXECUTE string, so function creation does not require those tables to
// exist; the test still guards the up/down shape and the exact signatures the
// registry SQL depends on (see L-150).
func TestLastSeenFunctionsMigrationUpAndDown(t *testing.T) {
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

	schema := "lastseen_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	up, err := migrationsFS.ReadFile("migrations/0093_last_seen_time_functions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile("migrations/0093_last_seen_time_functions.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, "SET search_path TO "+schema+"; "+string(up)); err != nil {
		t.Fatalf("apply up migration: %v", err)
	}
	for _, sig := range []string{
		"last_telemetry_time(uuid)",
		"last_presence_time(uuid)",
	} {
		var oid int
		if err := pool.QueryRow(ctx,
			"SELECT to_regprocedure($1)::oid", schema+"."+sig).Scan(&oid); err != nil {
			t.Fatalf("lookup %s: %v", sig, err)
		}
		if oid == 0 {
			t.Fatalf("up migration did not create %s", sig)
		}
	}

	if _, err := pool.Exec(ctx, "SET search_path TO "+schema+"; "+string(down)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	for _, sig := range []string{
		"last_telemetry_time(uuid)",
		"last_presence_time(uuid)",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT to_regprocedure($1) IS NOT NULL", schema+"."+sig).Scan(&exists); err != nil {
			t.Fatalf("lookup %s after down: %v", sig, err)
		}
		if exists {
			t.Fatalf("down migration did not drop %s", sig)
		}
	}
}
