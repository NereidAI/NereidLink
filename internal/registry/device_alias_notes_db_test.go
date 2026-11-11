// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// device_alias_notes_db_test.go verifies persistence and readback of the
// operator-facing alias/notes text fields and the computed last_activity_at
// timestamp (latest telemetry or presence boundary, whichever is newer)
// against a real PostgreSQL.

package registry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

const deviceMetaTestTenant = "00000000-0000-0000-0000-00000000d757"

func resetDeviceMetaFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, _ = pool.Exec(ctx, `DELETE FROM ts_telemetry WHERE tenant_id=$1`, deviceMetaTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM ts_presence WHERE tenant_id=$1`, deviceMetaTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device_ownership_event WHERE tenant_id=$1`, deviceMetaTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device WHERE tenant_id=$1`, deviceMetaTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM audit_event WHERE tenant_id=$1`, deviceMetaTestTenant)
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'device-meta-test')
		 ON CONFLICT (id) DO NOTHING`, deviceMetaTestTenant); err != nil {
		t.Fatal(err)
	}
}

// TestDeviceAliasNotesAndLastActivity exercises create/update/readback of
// alias and notes, alias-based search, and the activity-time computation.
func TestDeviceAliasNotesAndLastActivity(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(deviceMetaTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetDeviceMetaFixture(t, ctx, pool)
	t.Cleanup(func() { resetDeviceMetaFixture(t, context.Background(), pool) })

	created, err := admin.CreateDevice(ctx, deviceMetaTestTenant, DeviceDTO{
		CanonicalName: "meta-device-001",
		State:         StateActive,
		Alias:         "Warehouse sensor",
		Notes:         "Rack 14\nContact: ops@example.com",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	if created.Alias != "Warehouse sensor" || created.Notes == "" {
		t.Fatalf("create must read back alias/notes, got %+v", created)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("create must return created/updated timestamps, got %+v", created)
	}

	// Search matches both canonical_name and alias.
	byAlias, err := admin.ListDevices(ctx, deviceMetaTestTenant,
		DeviceFilter{Search: "warehouse"}, 1, 50)
	if err != nil {
		t.Fatalf("ListDevices alias search: %v", err)
	}
	if len(byAlias.Items) != 1 || byAlias.Items[0].ID != created.ID {
		t.Fatalf("alias search must find the device, got %d rows", len(byAlias.Items))
	}

	// Telemetry drives last_activity_at when there is no presence boundary.
	teleTime := time.Now().Add(-2 * time.Hour).UTC()
	if _, err := pool.Exec(ctx,
		`INSERT INTO ts_telemetry
		    (time, tenant_id, device_id, measurement_key, v_double)
		 VALUES ($1,$2,$3,'temp',21.5)`,
		teleTime, deviceMetaTestTenant, created.ID); err != nil {
		t.Fatal(err)
	}
	page, err := admin.ListDevices(ctx, deviceMetaTestTenant, DeviceFilter{}, 1, 50)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	row := page.Items[0]
	wantTele := teleTime.Truncate(time.Second).Format(time.RFC3339)
	if !sameRFC3339Second(row.LastActivityAt, wantTele) {
		t.Fatalf("last_activity must come from telemetry %s, got %q", wantTele, row.LastActivityAt)
	}

	// A newer presence boundary wins.
	presenceTime := time.Now().Add(-30 * time.Minute).UTC()
	if _, err := pool.Exec(ctx,
		`INSERT INTO ts_presence
		    (time, tenant_id, device_id, state, reason, session_id)
		 VALUES ($1,$2,$3,'ONLINE','connect','sess-1')`,
		presenceTime, deviceMetaTestTenant, created.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := admin.GetDevice(ctx, deviceMetaTestTenant, created.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	wantPresence := presenceTime.Truncate(time.Second).Format(time.RFC3339)
	if !sameRFC3339Second(detail.LastActivityAt, wantPresence) {
		t.Fatalf("last_activity must follow newer presence %s, got %q",
			wantPresence, detail.LastActivityAt)
	}
	if detail.Alias != "Warehouse sensor" || detail.Notes == "" || detail.CreatedAt == "" {
		t.Fatalf("detail must expose alias/notes/created_at, got %+v", detail)
	}

	// Updating with empty strings clears the optional text fields.
	created.CanonicalName = "meta-device-001"
	created.Alias = ""
	created.Notes = ""
	if _, err := admin.UpdateDevice(ctx, deviceMetaTestTenant, created.ID, created); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	cleared, err := admin.GetDevice(ctx, deviceMetaTestTenant, created.ID)
	if err != nil {
		t.Fatalf("GetDevice after update: %v", err)
	}
	if cleared.Alias != "" || cleared.Notes != "" {
		t.Fatalf("blank alias/notes must clear stored values, got %+v", cleared)
	}

	// Validation rejects oversized free-form text.
	long := make([]byte, 4097)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := admin.UpdateDevice(ctx, deviceMetaTestTenant, created.ID, DeviceDTO{
		CanonicalName: "meta-device-001",
		Notes:         string(long),
	}); err == nil {
		t.Fatal("notes over 4096 chars must be rejected")
	}
}

// sameRFC3339Second compares two RFC3339 timestamps at one-second resolution,
// which is enough for the activity computation (postgres stores microseconds,
// but the fixtures only assert the fixture second).
func sameRFC3339Second(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return false
	}
	return ta.Truncate(time.Second).Equal(tb.Truncate(time.Second))
}
