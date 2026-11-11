// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_db_test.go proves against a real PostgreSQL that PutAttribute
// persists the row and fans one server/shared change out via the optional
// AttributePublisher, while CLIENT scope stays device-owned (rejected before
// any publish).
package registry

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/attributeevent"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

type capturingPublisher struct {
	frames []mqttapi.AttributePushFrame
}

func (p *capturingPublisher) PublishAttributePush(frame mqttapi.AttributePushFrame) {
	p.frames = append(p.frames, frame)
}

func newAttributeFixture(t *testing.T) (*Admin, *pgxpool.Pool, string, string) {
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

	admin := NewAdmin(pool)
	tenant := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'attr-push')`, tenant); err != nil {
		t.Fatal(err)
	}
	dev, err := admin.CreateDevice(ctx, tenant, DeviceDTO{
		CanonicalName: "attr-target", State: StateActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM device_attribute WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM device WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id = $1`, tenant)
	})
	return admin, pool, tenant, dev.ID
}

func TestPutAttributePublishesPushFrame(t *testing.T) {
	admin, _, tenant, device := newAttributeFixture(t)
	publisher := &capturingPublisher{}
	admin.SetAttributePublisher(publisher)

	value := json.RawMessage(`42`)
	if err := admin.PutAttribute(context.Background(),
		tenant, device, ScopeShared, "maxTemp", value); err != nil {
		t.Fatalf("put attribute: %v", err)
	}
	if len(publisher.frames) != 1 {
		t.Fatalf("expected 1 push frame, got %d", len(publisher.frames))
	}
	frame := publisher.frames[0]
	if frame.TenantID != tenant || frame.DeviceID != device ||
		frame.Scope != ScopeShared || frame.Key != "maxTemp" ||
		string(frame.Value) != "42" || frame.UpdatedAt.IsZero() {
		t.Fatalf("unexpected push frame: %+v", frame)
	}
}

func TestPutAttributeRejectsClientScopeWithoutPublish(t *testing.T) {
	admin, _, tenant, device := newAttributeFixture(t)
	publisher := &capturingPublisher{}
	admin.SetAttributePublisher(publisher)

	err := admin.PutAttribute(context.Background(),
		tenant, device, "CLIENT", "k", json.RawMessage(`1`))
	if err == nil {
		t.Fatal("CLIENT scope must be rejected for platform writes")
	}
	if len(publisher.frames) != 0 {
		t.Fatalf("rejected write must not publish, got %d frames", len(publisher.frames))
	}
}

func TestPutAttributeWithoutPublisherStillPersists(t *testing.T) {
	admin, pool, tenant, device := newAttributeFixture(t)
	if err := admin.PutAttribute(context.Background(),
		tenant, device, ScopeServer, "mode", json.RawMessage(`"safe"`)); err != nil {
		t.Fatalf("put attribute: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM device_attribute
		 WHERE tenant_id=$1 AND device_id=$2 AND scope='SERVER_SCOPE' AND key='mode'`,
		tenant, device).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attribute not persisted, count=%d err=%v", count, err)
	}
}

type capturingAttributeEventPublisher struct {
	events []attributeevent.Event
}

func (p *capturingAttributeEventPublisher) PublishAttributeEvent(e attributeevent.Event) {
	p.events = append(p.events, e)
}

func TestFetchAttributesReturnsPlatformScopes(t *testing.T) {
	admin, _, tenant, device := newAttributeFixture(t)
	ctx := context.Background()
	if err := admin.PutAttribute(ctx, tenant, device, ScopeShared, "a", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := admin.PutAttribute(ctx, tenant, device, ScopeServer, "b", json.RawMessage(`"x"`)); err != nil {
		t.Fatal(err)
	}

	got, err := admin.FetchAttributes(ctx, tenant, device, nil)
	if err != nil {
		t.Fatalf("fetch both scopes: %v", err)
	}
	if string(got[ScopeShared]["a"]) != "1" || string(got[ScopeServer]["b"]) != `"x"` {
		t.Fatalf("unexpected fetch payload: %+v", got)
	}

	only, err := admin.FetchAttributes(ctx, tenant, device, []string{ScopeShared})
	if err != nil || len(only) != 1 || only[ScopeShared] == nil {
		t.Fatalf("scoped fetch wrong: %+v err=%v", only, err)
	}

	if _, err := admin.FetchAttributes(ctx, tenant, device, []string{"CLIENT"}); err == nil {
		t.Fatal("CLIENT scope must be rejected from device fetch")
	}
}

func TestDeleteAttributeRemovesRowAndPublishesNull(t *testing.T) {
	admin, _, tenant, device := newAttributeFixture(t)
	ctx := context.Background()
	publisher := &capturingPublisher{}
	events := &capturingAttributeEventPublisher{}
	admin.SetAttributePublisher(publisher)
	admin.SetAttributeEventPublisher(events)

	if err := admin.PutAttribute(ctx, tenant, device, ScopeShared, "k", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := admin.DeleteAttribute(ctx, tenant, device, ScopeShared, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := admin.DeleteAttribute(ctx, tenant, device, ScopeShared, "k"); err == nil {
		t.Fatal("deleting a missing attribute must return not found")
	}
	if err := admin.DeleteAttribute(ctx, tenant, device, "CLIENT", "k"); err == nil {
		t.Fatal("CLIENT scope must not be deletable by the platform")
	}

	// Last pushed frame (after the upsert frame) carries the null marker.
	if len(publisher.frames) != 2 {
		t.Fatalf("want 2 push frames (upsert+delete), got %d", len(publisher.frames))
	}
	if string(publisher.frames[1].Value) != "null" {
		t.Fatalf("delete push must carry null, got %s", publisher.frames[1].Value)
	}
	if len(events.events) != 2 || string(events.events[1].Value) != "null" {
		t.Fatalf("want delete console event with null, got %+v", events.events)
	}
}
