// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stats_db_test.go executes Current against a real database. The gauge query
// spans many tables via correlated subqueries; a wrong table or column name
// compiles fine and fails only at query time, which is why this is a database
// test rather than a unit test.

package stats

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCurrentQueryExecutes(t *testing.T) {
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'stats-gauge-test')
		 ON CONFLICT DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id = $1`, tenant)
	})

	svc := &Service{pool: pool}
	rows, err := svc.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	for _, g := range rows {
		if g.TenantID == tenant {
			// A fresh tenant has zero of every gauge; the point is that every
			// correlated subquery ran.
			if g.DevicesTotal != 0 || g.Customers != 0 || g.OTATasks != 0 {
				t.Fatalf("fresh tenant gauge not zero: %+v", g)
			}
			return
		}
	}
	t.Fatal("created tenant not present in Current()")
}
