// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_db_test.go proves device-event ingest persists to device_event and
// that List narrows by the caller's data scope the same way alarm/telemetry
// lists do, so a customer-scoped caller cannot read another customer's
// device events.
package deviceevent

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

type eventFixture struct {
	pool    *pgxpool.Pool
	service *Service
	tenant  string
	custA   string
	custB   string
	devA    string
	devB    string
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
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

	f := &eventFixture{
		pool:    pool,
		service: New(pool, nil),
		tenant:  uuid.NewString(),
		custA:   uuid.NewString(),
		custB:   uuid.NewString(),
		devA:    uuid.NewString(),
		devB:    uuid.NewString(),
	}
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	must(`INSERT INTO tenant (id,name) VALUES ($1,'device-event')`, f.tenant)
	must(`INSERT INTO customer (id,tenant_id,name) VALUES ($1,$2,'A'),($3,$2,'B')`,
		f.custA, f.tenant, f.custB)
	must(`INSERT INTO device (id,tenant_id,canonical_name,customer_id,state)
	      VALUES ($1,$2,'ea',$3,'ACTIVE'),($4,$2,'eb',$5,'ACTIVE')`,
		f.devA, f.tenant, f.custA, f.devB, f.custB)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM device_event WHERE tenant_id=$1`, f.tenant)
		_, _ = pool.Exec(bg, `DELETE FROM device WHERE tenant_id=$1`, f.tenant)
		_, _ = pool.Exec(bg, `DELETE FROM customer WHERE tenant_id=$1`, f.tenant)
		_, _ = pool.Exec(bg, `DELETE FROM tenant WHERE id=$1`, f.tenant)
	})
	return f
}

func TestIngestPersistsAndDefaultsSeverity(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	err := f.service.Ingest(ctx, f.tenant, f.devA, []Frame{
		{EventType: "door_opened", Payload: map[string]any{"by": "badge-7"}},
		{EventType: "temp_alarm", Severity: SeverityCritical, Payload: map[string]any{"c": 91}},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	events, err := f.service.List(ctx, Query{TenantID: f.tenant, DeviceID: f.devA},
		rbac.UnrestrictedScope(f.tenant))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// Newest first: the critical frame was inserted second.
	if events[0].EventType != "temp_alarm" || events[0].Severity != SeverityCritical {
		t.Fatalf("unexpected newest event: %+v", events[0])
	}
	if events[1].Severity != SeverityInfo {
		t.Fatalf("missing severity should default to info, got %q", events[1].Severity)
	}
}

func TestIngestRejectsBadFrame(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	cases := [][]Frame{
		{{EventType: "", Payload: nil}},
		{{EventType: "x", Severity: "fatal"}},
	}
	for index, frames := range cases {
		if err := f.service.Ingest(ctx, f.tenant, f.devA, frames); err == nil {
			t.Fatalf("case %d should be rejected", index)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM device_event WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected batch must write nothing, got %d rows", count)
	}
}

func TestListNarrowsByCustomerScope(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	if err := f.service.Ingest(ctx, f.tenant, f.devA, []Frame{{EventType: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Ingest(ctx, f.tenant, f.devB, []Frame{{EventType: "b"}}); err != nil {
		t.Fatal(err)
	}

	scopeA := rbac.DataScope{
		Level: rbac.ScopeCustomer, TenantID: f.tenant, CustomerID: f.custA,
	}
	// Tenant-wide list under A's scope sees only A's device event.
	events, err := f.service.List(ctx, Query{TenantID: f.tenant}, scopeA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "a" {
		t.Fatalf("scope A should see only its event, got %+v", events)
	}

	// Explicitly asking for B's device returns nothing rather than its rows.
	events, err = f.service.List(ctx, Query{TenantID: f.tenant, DeviceID: f.devB}, scopeA)
	if err != nil {
		t.Fatalf("list foreign: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("scope A must not read B's device events, got %+v", events)
	}
}

func TestEventTypeFilter(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	if err := f.service.Ingest(ctx, f.tenant, f.devA, []Frame{
		{EventType: "open"}, {EventType: "close"}, {EventType: "open"},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := f.service.List(ctx,
		Query{TenantID: f.tenant, DeviceID: f.devA, EventType: "open"},
		rbac.UnrestrictedScope(f.tenant))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 open events, got %d", len(events))
	}
}
