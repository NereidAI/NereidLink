// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go verifies forwarder management invariants. The sink-type
// immutability check returns before touching JetStream or writing, so it is
// testable against a real database without a JetStream connection.

package egress

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUpdateRejectsSinkTypeChange ensures an edit cannot change the sink
// type of an existing forwarder.
func TestUpdateRejectsSinkTypeChange(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping forwarder update integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	store := NewStore(pool)

	// Find any existing forwarder, regardless of tenant; only its Get
	// path is exercised and nothing is written.
	rows, err := pool.Query(ctx,
		`SELECT tenant_id::text, id::text, sink_type FROM forwarder LIMIT 1`)
	if err != nil {
		t.Fatalf("query forwarder: %v", err)
	}
	var tenantID, forwarderID, sinkType string
	if !rows.Next() {
		rows.Close()
		t.Skip("no forwarder rows present; create one before running this test")
	}
	if err := rows.Scan(&tenantID, &forwarderID, &sinkType); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rows.Close()

	// js is nil on purpose: a rejected update must return before any
	// JetStream call.
	svc := NewService(nil, store, 1)

	otherType := "mqtt"
	if sinkType == "mqtt" {
		otherType = "nats"
	}
	_, err = svc.Update(ctx, tenantID, forwarderID, Input{
		Name:     "sink-type-change-attempt",
		SinkType: otherType,
	})
	if err == nil {
		t.Fatal("changing sink type must fail")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument, got %v", err)
	}

	// The stored row must be untouched.
	f, err := store.Get(ctx, tenantID, forwarderID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if f.SinkType != sinkType {
		t.Fatalf("sink type changed in storage: was %s now %s", sinkType, f.SinkType)
	}
}
