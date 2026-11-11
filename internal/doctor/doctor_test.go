// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// doctor_test.go covers report aggregation and validates the complete check
// set against an opt-in PostgreSQL/TimescaleDB database.

package doctor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects when NL_TEST_DATABASE_URL is configured and skips
// otherwise.
func testPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// TestReportHasFailure verifies only FAIL observations affect process status.
func TestReportHasFailure(t *testing.T) {
	if (Report{{Level: LevelPass}, {Level: LevelAdvisory}}).HasFailure() {
		t.Error("pass and advisory checks were treated as failures")
	}
	if !(Report{{Level: LevelPass}, {Level: LevelFail}}).HasFailure() {
		t.Error("a fail observation did not make the report unhealthy")
	}
}

// TestRunValidatesHealthyDatabase verifies all required ADR-0004 checks pass
// against a prepared database. A fresh disposable marker is inserted so the
// test does not depend on prior operator backup history.
func TestRunValidatesHealthyDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var markerID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO backup_marker (scope, details, succeeded_at)
		 VALUES ('postgres', '{"test":true}', now())
		 RETURNING id`).Scan(&markerID); err != nil {
		t.Fatalf("insert backup marker: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM backup_marker WHERE id = $1`, markerID); err != nil {
			t.Logf("delete backup marker: %v", err)
		}
	})

	report := Run(ctx, pool, 24*time.Hour)
	for _, check := range report {
		if check.Level == LevelFail {
			t.Errorf("required check failed: %s %s", check.Name, check.Message)
		}
	}
}

// TestRunDisabledBackupCheckIsAdvisory verifies a zero threshold disables
// backup age enforcement while still producing an explicit advisory line.
func TestRunDisabledBackupCheckIsAdvisory(t *testing.T) {
	pool := testPool(t)
	report := Run(context.Background(), pool, 0)
	for _, check := range report {
		if check.Name == "backup marker" {
			if check.Level != LevelAdvisory {
				t.Fatalf("disabled backup check level = %s", check.Level)
			}
			return
		}
	}
	t.Fatal("backup marker check missing")
}
