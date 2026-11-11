// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// keys_series_scope_db_test.go proves the measurement-key strip and the
// downsampled series endpoint are narrowed by data scope, like raw history.
// They return no device row themselves, so a scope omission is silent: the
// query simply succeeds against another customer's device. Executing both
// under a CUSTOMER scope and expecting empty results is the only way to prove
// the gate is present.

package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// seedKeysSeriesFixture builds a tenant with two customers, one device each,
// and one measurement per device. Fixed ids are namespaced apart from the
// history fixture so the files can run in the same database.
func seedKeysSeriesFixture(t *testing.T) (srv *Server, pool *pgxpool.Pool, tenant, devA, devB string) {
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

	ctx := context.Background()
	tenant = "00000000-0000-0000-0000-00000000bd01"
	customerA := "00000000-0000-0000-0000-00000000bd02"
	customerB := "00000000-0000-0000-0000-00000000bd03"
	devA = "00000000-0000-0000-0000-00000000bd04"
	devB = "00000000-0000-0000-0000-00000000bd05"

	must := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(`INSERT INTO tenant (id, name) VALUES ($1,'keys-series-scope')
	      ON CONFLICT (id) DO NOTHING`, tenant)
	must(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,'A'),($3,$2,'B')
	      ON CONFLICT (id) DO NOTHING`, customerA, tenant, customerB)
	must(`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
	      VALUES ($1,$2,'ks-a','ACTIVE',$3),($4,$2,'ks-b','ACTIVE',$5)
	      ON CONFLICT (id) DO UPDATE SET customer_id = EXCLUDED.customer_id, state='ACTIVE'`,
		devA, tenant, customerA, devB, customerB)

	now := time.Now().UTC()
	base := now.Add(-30 * time.Minute)
	for dev, val := range map[string]float64{devA: 21.0, devB: 99.0} {
		must(`INSERT INTO ts_telemetry (time, tenant_id, device_id, measurement_key, v_double)
		      SELECT $1::timestamptz + (n * interval '1 minute'), $2, $3, 'temperature', $4::double precision
		      FROM generate_series(0, 2) AS n`, base, tenant, dev, val)
	}

	t.Cleanup(func() {
		c := context.Background()
		for _, q := range []string{
			`DELETE FROM ts_telemetry WHERE tenant_id = $1`,
			`UPDATE device SET customer_id = NULL WHERE tenant_id = $1`,
			`DELETE FROM device WHERE tenant_id = $1`,
			`DELETE FROM customer WHERE tenant_id = $1`,
			`DELETE FROM tenant WHERE id = $1`,
		} {
			if _, err := pool.Exec(c, q, tenant); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	})
	return &Server{pool: pool}, pool, tenant, devA, devB
}

// TestKeysRespectsDataScope proves the key strip is empty for a foreign device
// and populated for the caller's own.
func TestKeysRespectsDataScope(t *testing.T) {
	srv, _, tenant, devA, devB := seedKeysSeriesFixture(t)
	ctx := context.Background()
	start, end := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(time.Hour)

	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level: rbac.ScopeCustomer, TenantID: tenant,
		CustomerID: "00000000-0000-0000-0000-00000000bd02",
	})

	keys, err := srv.Keys(narrow, tenant, devB, start, end)
	if err != nil {
		t.Fatalf("Keys foreign: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("LEAK: CUSTOMER scope saw %d keys of a foreign device", len(keys))
	}

	keys, err = srv.Keys(narrow, tenant, devA, start, end)
	if err != nil {
		t.Fatalf("Keys own: %v", err)
	}
	if len(keys) != 1 || keys[0].MeasurementKey != "temperature" {
		t.Fatalf("own device keys = %+v, want one temperature key", keys)
	}
}

// TestSeriesRespectsDataScope proves the aggregated series returns no bucket
// for a foreign device but does for the caller's own.
func TestSeriesRespectsDataScope(t *testing.T) {
	srv, _, tenant, devA, devB := seedKeysSeriesFixture(t)
	ctx := context.Background()
	start, end := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(time.Hour)

	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level: rbac.ScopeCustomer, TenantID: tenant,
		CustomerID: "00000000-0000-0000-0000-00000000bd02",
	})

	points, err := srv.Series(narrow, tenant, devB, "temperature", start, end)
	if err != nil {
		t.Fatalf("Series foreign: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("LEAK: CUSTOMER scope saw %d series buckets of a foreign device", len(points))
	}

	points, err = srv.Series(narrow, tenant, devA, "temperature", start, end)
	if err != nil {
		t.Fatalf("Series own: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("own device series returned no buckets")
	}
}
