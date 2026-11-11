// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// scope_db_test.go proves the alarm list narrows to the caller's data scope.
//
// The alarm list is the most-read device-derived view in the product, and it
// is the one a support engineer with a CUSTOMER scope opens all day. Two
// mistakes are invisible without executing the query: the count drifting out of
// sync with the page, and the scope predicate silently evaluating true (see
// L-062 and L-063). Both are asserted here against a real database.

package alarm

import (
	"context"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestListPageRespectsDataScope proves the page and its count are narrowed
// together, and that a foreign customer's alarm is absent from both.
func TestListPageRespectsDataScope(t *testing.T) {
	pool := ticketsTestPool(t)
	ctx := context.Background()

	tenantID := "00000000-0000-0000-0000-00000000ab01"
	customerA := "00000000-0000-0000-0000-00000000ab02"
	customerB := "00000000-0000-0000-0000-00000000ab03"
	deviceA := "00000000-0000-0000-0000-00000000ab04"
	deviceB := "00000000-0000-0000-0000-00000000ab05"
	deviceFree := "00000000-0000-0000-0000-00000000ab06"
	alarmA := "00000000-0000-0000-0000-00000000ab07"
	alarmB := "00000000-0000-0000-0000-00000000ab08"
	alarmFree := "00000000-0000-0000-0000-00000000ab09"

	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO tenant (id, name) VALUES ($1, 'alarm-scope-test')
		  ON CONFLICT (id) DO NOTHING`, []any{tenantID}},
		{`INSERT INTO customer (id, tenant_id, name)
		  VALUES ($1,$2,'custA'), ($3,$2,'custB')
		  ON CONFLICT (id) DO NOTHING`,
			[]any{customerA, tenantID, customerB}},
		{`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
		  VALUES ($1,$2,'dev-a','ACTIVE',$3),
		         ($4,$2,'dev-b','ACTIVE',$5),
		         ($6,$2,'dev-free','ACTIVE',NULL)
		  ON CONFLICT (id) DO UPDATE
		    SET customer_id = EXCLUDED.customer_id, state = 'ACTIVE'`,
			[]any{deviceA, tenantID, customerA, deviceB, customerB,
				deviceFree}},
		// Three live alarms with distinct dedupe keys, one per device.
		{`INSERT INTO alarm
		   (id, tenant_id, device_id, type, severity, state, dedupe_key)
		  VALUES ($1,$2,$3,'test','WARNING','ACTIVE','scope-a'),
		         ($4,$2,$5,'test','WARNING','ACTIVE','scope-b'),
		         ($6,$2,$7,'test','WARNING','ACTIVE','scope-free')
		  ON CONFLICT (id) DO NOTHING`,
			[]any{alarmA, tenantID, deviceA, alarmB, deviceB,
				alarmFree, deviceFree}},
	} {
		if _, err := pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, q := range []string{
			`DELETE FROM alarm WHERE tenant_id = $1`,
			// Ownership must be released before the customers can go.
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

	svc := NewService(pool, nil)

	ids := func(rows []Row) map[string]bool {
		out := make(map[string]bool, len(rows))
		for _, r := range rows {
			out[r.AlarmID] = true
		}
		return out
	}

	// TENANT scope sees every alarm in the tenant.
	full := rbac.ContextWithScope(ctx,
		rbac.DataScope{Level: rbac.ScopeTenant, TenantID: tenantID})
	page, err := svc.ListPage(full, tenantID, Filter{}, 1, 50)
	if err != nil {
		t.Fatalf("ListPage TENANT: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("TENANT scope total = %d, want 3", page.Total)
	}
	if len(ids(page.Items)) != 3 {
		t.Fatalf("TENANT scope rows = %d, want 3", len(page.Items))
	}

	// A CUSTOMER scope sees exactly its own customer's alarm, and the count
	// must agree with the rows: an empty table under a tenant-wide total is
	// itself a leak.
	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level:      rbac.ScopeCustomer,
		TenantID:   tenantID,
		CustomerID: customerA,
	})
	page, err = svc.ListPage(narrow, tenantID, Filter{}, 1, 50)
	if err != nil {
		t.Fatalf("ListPage CUSTOMER: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("CUSTOMER scope total = %d, want 1", page.Total)
	}
	if len(page.Items) != 1 {
		t.Fatalf("CUSTOMER scope returned %d rows, want 1", len(page.Items))
	}
	seen := ids(page.Items)
	if !seen[alarmA] {
		t.Fatalf("CUSTOMER scope did not return its own alarm %s", alarmA)
	}
	for _, hidden := range []string{alarmB, alarmFree} {
		if seen[hidden] {
			t.Fatalf("LEAK: CUSTOMER scope returned foreign alarm %s", hidden)
		}
	}

	// An unowned device is invisible to every CUSTOMER scope but still counts
	// for the tenant, so the two totals genuinely differ.
	if _, err := svc.ListPage(narrow, tenantID, Filter{DeviceID: deviceFree}, 1, 50); err != nil {
		t.Fatalf("ListPage filtered by a foreign device: %v", err)
	}
	page, err = svc.ListPage(narrow, tenantID, Filter{DeviceID: deviceFree}, 1, 50)
	if err != nil {
		t.Fatalf("ListPage re-read: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("CUSTOMER scope filtering by a foreign device returned %d rows, want 0",
			page.Total)
	}
}
