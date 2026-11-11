// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// history_scope_db_test.go proves the telemetry history query narrows to the
// caller's data scope.
//
// ts_telemetry is a hypertable with no tenant-scoped ownership column, so the
// only thing standing between a CUSTOMER-scoped support account and another
// customer's raw measurements is the scope predicate appended to this query.
// The count is a separate statement and is the usual place the narrowing is
// lost (L-063), so both are asserted against a real database.

package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestHistoryRespectsDataScope proves the page and its count are narrowed
// together and that foreign measurements never appear.
func TestHistoryRespectsDataScope(t *testing.T) {
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

	const (
		tenantID   = "00000000-0000-0000-0000-00000000ac01"
		customerA  = "00000000-0000-0000-0000-00000000ac02"
		customerB  = "00000000-0000-0000-0000-00000000ac03"
		deviceA    = "00000000-0000-0000-0000-00000000ac04"
		deviceB    = "00000000-0000-0000-0000-00000000ac05"
		deviceFree = "00000000-0000-0000-0000-00000000ac06"
	)

	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO tenant (id, name) VALUES ($1, 'telemetry-scope-test')
		  ON CONFLICT (id) DO NOTHING`, []any{tenantID}},
		{`INSERT INTO customer (id, tenant_id, name)
		  VALUES ($1,$2,'tA'), ($3,$2,'tB')
		  ON CONFLICT (id) DO NOTHING`,
			[]any{customerA, tenantID, customerB}},
		{`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
		  VALUES ($1,$2,'t-dev-a','ACTIVE',$3),
		         ($4,$2,'t-dev-b','ACTIVE',$5),
		         ($6,$2,'t-dev-free','ACTIVE',NULL)
		  ON CONFLICT (id) DO UPDATE
		    SET customer_id = EXCLUDED.customer_id, state = 'ACTIVE'`,
			[]any{deviceA, tenantID, customerA, deviceB, customerB, deviceFree}},
	} {
		if _, err := pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Two points per device, well inside any window the test will ask for.
	now := time.Now().UTC()
	base := now.Add(-time.Hour)
	if _, err := pool.Exec(ctx,
		`INSERT INTO ts_telemetry (time, tenant_id, device_id, measurement_key, v_double)
		 SELECT $1::timestamptz + (n * interval '1 minute'), $2, $3,
		        'temperature', 20.0 + n::double precision
		 FROM generate_series(0, 1) AS n`,
		base, tenantID, deviceA); err != nil {
		t.Fatalf("insert telemetry a: %v", err)
	}
	for _, dev := range []string{deviceB, deviceFree} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO ts_telemetry (time, tenant_id, device_id, measurement_key, v_double)
			 SELECT $1::timestamptz + (n * interval '1 minute'), $2, $3,
			        'temperature', 99.0 + n::double precision
			 FROM generate_series(0, 1) AS n`,
			base, tenantID, dev); err != nil {
			t.Fatalf("insert telemetry %s: %v", dev, err)
		}
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
			if _, err := pool.Exec(c, q, tenantID); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	srv := &Server{pool: pool}
	start, end := now.Add(-2*time.Hour), now.Add(time.Hour)

	full := rbac.ContextWithScope(ctx,
		rbac.DataScope{Level: rbac.ScopeTenant, TenantID: tenantID})
	page, err := srv.History(full, tenantID, deviceA, "temperature", start, end, 1, 50)
	if err != nil {
		t.Fatalf("History TENANT: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("TENANT scope total = %d, want 2", page.Total)
	}

	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level:      rbac.ScopeCustomer,
		TenantID:   tenantID,
		CustomerID: customerA,
	})

	// Reading one's own device still works under a narrow scope.
	page, err = srv.History(narrow, tenantID, deviceA, "temperature", start, end, 1, 50)
	if err != nil {
		t.Fatalf("History CUSTOMER on own device: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("own device under CUSTOMER scope = %d/%d, want 2/2",
			page.Total, len(page.Items))
	}

	// Reading a foreign device must come back empty rather than forbidden or,
	// worse, populated. A non-empty result here is the leak.
	for _, foreign := range []string{deviceB, deviceFree} {
		page, err = srv.History(narrow, tenantID, foreign,
			"temperature", start, end, 1, 50)
		if err != nil {
			t.Fatalf("History CUSTOMER on %s: %v", foreign, err)
		}
		if page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("LEAK: CUSTOMER scope read %d/%d points of foreign device %s",
				page.Total, len(page.Items), foreign)
		}
	}
}
