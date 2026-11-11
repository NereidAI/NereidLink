// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gateway_persistent_db_test.go proves a persistent command targeted at a
// gateway-attached sub-device is claimed on the sub-device's ONLINE boundary
// exactly like a direct device command. The gateway itself contributes only
// presence.GatewayID (covered end-to-end by the MQTT relay test and carried on
// the wire by toWireCommand); the durable queue is keyed by tenant+device, so
// a sub-device must be reclaimable here with no gateway-specific branch.
package command

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newGatewayPersistentStore(t *testing.T) (*Store, *pgxpool.Pool, string, string) {
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
	tenant := uuid.NewString()
	sub := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'gw-persistent')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state)
		 VALUES ($1,$2,'gw-sub','ACTIVE')`, sub, tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM command WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(bg, `DELETE FROM device WHERE id=$1`, sub)
		_, _ = pool.Exec(bg, `DELETE FROM tenant WHERE id=$1`, tenant)
	})
	return NewStore(pool), pool, tenant, sub
}

func TestPersistentSubdeviceCommandClaimedOnReconnect(t *testing.T) {
	store, _, tenant, sub := newGatewayPersistentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A persistent command queued while the sub-device (and its gateway) are
	// offline stays PENDING with a TTL deadline.
	queued := Command{
		ID:          uuid.NewString(),
		TenantID:    tenant,
		DeviceID:    sub,
		Name:        "reboot",
		Params:      []byte(`{"window":"02:00"}`),
		Interaction: InteractionOneWay,
		Timeliness:  TimelinessPersistent,
		Timeout:     30 * time.Second,
		CreatedAt:   now,
	}
	if err := store.Insert(ctx, queued, StatusPending, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert persistent command: %v", err)
	}

	// The sub-device ONLINE boundary (published by gateway connect) claims it.
	claimed, err := store.ClaimPending(ctx, tenant, sub, "router-1", 10)
	if err != nil {
		t.Fatalf("claim pending: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed command, got %d", len(claimed))
	}
	got := claimed[0]
	if got.DeviceID != sub || got.Timeliness != TimelinessPersistent ||
		got.Name != "reboot" || got.ID != queued.ID {
		t.Fatalf("claimed command mismatch: %+v", got)
	}

	// A concurrent claim on another router replica must skip the locked/claimed
	// row, preventing double delivery through the gateway.
	again, err := store.ClaimPending(ctx, tenant, sub, "router-2", 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("claimed command must not be reclaimable, got %d", len(again))
	}

	// Successful gateway delivery terminalizes the row like a direct device.
	if err := store.Complete(ctx, queued.ID, StatusDelivered, nil, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var state string
	if err := store.pool.QueryRow(ctx,
		`SELECT state FROM command WHERE id=$1`, queued.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StatusDelivered {
		t.Fatalf("state = %q, want %q", state, StatusDelivered)
	}
}
