// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// action_scope_db_test.go proves the alarm write/action surface is narrowed by
// data scope, not just the list page. Acknowledging, assigning and commenting
// are reachable by alarm id alone, so without a device-visibility predicate a
// customer-scoped user could act on another customer's alarms. Each negative
// case asserts ErrNotFound and that the foreign row is untouched.

package alarm

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// actionScopeFixture is a tenant with two customers, one ACTIVE alarm per
// customer's device and one user (for assignment/comment foreign keys).
type actionScopeFixture struct {
	tenant    string
	user      string
	customerA string
	customerB string
	alarmA    string
	alarmB    string
}

func newActionScopeFixture(t *testing.T) (*Service, actionScopeFixture) {
	t.Helper()
	pool := ticketsTestPool(t)
	ctx := context.Background()

	f := actionScopeFixture{
		tenant:    "00000000-0000-0000-0000-00000000ac01",
		user:      "00000000-0000-0000-0000-00000000ac10",
		customerA: "00000000-0000-0000-0000-00000000ac20",
		customerB: "00000000-0000-0000-0000-00000000ac21",
		alarmA:    "00000000-0000-0000-0000-00000000ac30",
		alarmB:    "00000000-0000-0000-0000-00000000ac31",
	}
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(`INSERT INTO tenant (id, name) VALUES ($1,'alarm-action-scope')
	      ON CONFLICT (id) DO NOTHING`, f.tenant)
	must(`INSERT INTO app_user (id, tenant_id, email, display_name, email_verified)
	      VALUES ($1,$2,'action-scope@example.test','Scope User',true)
	      ON CONFLICT (id) DO UPDATE SET disabled = false`, f.user, f.tenant)
	must(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,'A'),($3,$2,'B')
	      ON CONFLICT (id) DO NOTHING`, f.customerA, f.tenant, f.customerB)
	must(`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
	      VALUES ($1,$2,'ac-a','ACTIVE',$3),($4,$2,'ac-b','ACTIVE',$5)
	      ON CONFLICT (id) DO UPDATE SET customer_id = EXCLUDED.customer_id, state='ACTIVE'`,
		"00000000-0000-0000-0000-00000000ac40", f.tenant, f.customerA,
		"00000000-0000-0000-0000-00000000ac41", f.customerB)
	must(`INSERT INTO alarm (id, tenant_id, device_id, type, severity, state, dedupe_key)
	      VALUES ($1,$2,$3,'test','WARNING','ACTIVE','ac-a'),
	             ($4,$2,$5,'test','WARNING','ACTIVE','ac-b')
	      ON CONFLICT (id) DO UPDATE SET state='ACTIVE', assignee_id=NULL`,
		f.alarmA, f.tenant, "00000000-0000-0000-0000-00000000ac40",
		f.alarmB, "00000000-0000-0000-0000-00000000ac41")

	t.Cleanup(func() {
		c := context.Background()
		clean := func(q string, args ...any) {
			if _, err := pool.Exec(c, q, args...); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
		clean(`DELETE FROM alarm_comment WHERE tenant_id = $1`, f.tenant)
		clean(`DELETE FROM alarm WHERE tenant_id = $1`, f.tenant)
		clean(`UPDATE device SET customer_id = NULL WHERE tenant_id = $1`, f.tenant)
		clean(`DELETE FROM device WHERE tenant_id = $1`, f.tenant)
		clean(`DELETE FROM customer WHERE tenant_id = $1`, f.tenant)
		clean(`DELETE FROM app_user WHERE id = $1`, f.user)
		clean(`DELETE FROM tenant WHERE id = $1`, f.tenant)
	})
	return NewService(pool, nil), f
}

// customerCtx returns a context narrowed to customer A.
func (f actionScopeFixture) customerCtx() context.Context {
	return rbac.ContextWithScope(context.Background(), rbac.DataScope{
		Level: rbac.ScopeCustomer, TenantID: f.tenant, CustomerID: f.customerA,
	})
}

func (f actionScopeFixture) alarmState(t *testing.T, svc *Service, id string) string {
	t.Helper()
	var state string
	if err := svc.pool.QueryRow(context.Background(),
		`SELECT state FROM alarm WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestAckAlarmRespectsScope proves a foreign alarm cannot be acknowledged and
// stays ACTIVE, while the caller's own alarm is acked.
func TestAckAlarmRespectsScope(t *testing.T) {
	svc, f := newActionScopeFixture(t)

	if err := svc.AckAlarm(f.customerCtx(), f.tenant, f.alarmB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AckAlarm foreign want ErrNotFound, got %v", err)
	}
	if got := f.alarmState(t, svc, f.alarmB); got != "ACTIVE" {
		t.Fatalf("foreign alarm state changed to %q despite denial", got)
	}
	if err := svc.AckAlarm(f.customerCtx(), f.tenant, f.alarmA); err != nil {
		t.Fatalf("AckAlarm own: %v", err)
	}
	if got := f.alarmState(t, svc, f.alarmA); got != "ACK" {
		t.Fatalf("own alarm = %q, want ACK", got)
	}
}

// TestAssignAlarmRespectsScope proves assignment cannot be moved on a foreign
// alarm.
func TestAssignAlarmRespectsScope(t *testing.T) {
	svc, f := newActionScopeFixture(t)

	if err := svc.AssignAlarm(f.customerCtx(), f.tenant, f.alarmB, f.user); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AssignAlarm foreign want ErrNotFound, got %v", err)
	}
	var assignee *string
	if err := svc.pool.QueryRow(context.Background(),
		`SELECT assignee_id::text FROM alarm WHERE id = $1`, f.alarmB).Scan(&assignee); err != nil {
		t.Fatal(err)
	}
	if assignee != nil {
		t.Fatalf("foreign alarm assigned to %s despite denial", *assignee)
	}
}

// TestCommentRespectsScope proves commenting and reading a foreign alarm's
// thread are both refused as not-found.
func TestCommentRespectsScope(t *testing.T) {
	svc, f := newActionScopeFixture(t)
	ctx := f.customerCtx()

	if _, err := svc.AddComment(ctx, f.tenant, f.alarmB, f.user, "pwn"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddComment foreign want ErrNotFound, got %v", err)
	}
	var n int
	if err := svc.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM alarm_comment WHERE alarm_id = $1`, f.alarmB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("foreign alarm received %d comments despite denial", n)
	}
	if _, err := svc.ListComments(ctx, f.tenant, f.alarmB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListComments foreign want ErrNotFound, got %v", err)
	}

	// The caller's own alarm accepts a comment and the thread is readable.
	if _, err := svc.AddComment(ctx, f.tenant, f.alarmA, f.user, "mine"); err != nil {
		t.Fatalf("AddComment own: %v", err)
	}
	comments, err := svc.ListComments(ctx, f.tenant, f.alarmA)
	if err != nil {
		t.Fatalf("ListComments own: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "mine" {
		t.Fatalf("own thread = %+v", comments)
	}
}
