// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go exercises migration 0091 against real PostgreSQL. It is
// opt-in through NL_TEST_DATABASE_URL and uses a fixed throwaway tenant whose
// rows are removed before and after the run.

package companion

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTestTenant = "00000000-0000-0000-0000-00000000d901"

func TestPGStoreCompanionLifecycle(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewPGStore(pool)

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM companion_endpoint WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'companion-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}

	// Upsert insert, natural-key lookup and surrogate-id lookup.
	ep, err := store.UpsertEndpoint(ctx, Endpoint{
		TenantID:        dbTestTenant,
		CompanionID:     "face-service",
		GatewayID:       "gateway-1",
		BaseURL:         "http://companion-1",
		ContractVersion: ContractV1,
		AuthToken:       "token-1",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	if ep.ID == "" || ep.CreatedAt.IsZero() || ep.UpdatedAt.IsZero() {
		t.Fatalf("stored endpoint missing metadata: %+v", ep)
	}
	got, err := store.GetEndpoint(ctx, dbTestTenant, "face-service", "gateway-1")
	if err != nil || got.BaseURL != "http://companion-1" {
		t.Fatalf("GetEndpoint = %+v, %v", got, err)
	}
	byID, err := store.GetEndpointByID(ctx, dbTestTenant, ep.ID)
	if err != nil || byID.CompanionID != "face-service" {
		t.Fatalf("GetEndpointByID = %+v, %v", byID, err)
	}
	if _, err := store.GetEndpoint(ctx, "00000000-0000-0000-0000-0000000000ff",
		"face-service", "gateway-1"); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("cross-tenant lookup err = %v, want ErrStoreNotFound", err)
	}

	// Upsert update keeps the surrogate id and applies the new fields.
	updated, err := store.UpsertEndpoint(ctx, Endpoint{
		TenantID: dbTestTenant, CompanionID: "face-service", GatewayID: "gateway-1",
		BaseURL: "http://companion-2", ContractVersion: ContractV1, Enabled: false,
	})
	if err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	if updated.ID != ep.ID || updated.BaseURL != "http://companion-2" || updated.Enabled {
		t.Fatalf("upsert update mismatch: %+v", updated)
	}
	endpoints, err := store.ListEndpoints(ctx, dbTestTenant)
	if err != nil || len(endpoints) != 1 || endpoints[0].ID != ep.ID {
		t.Fatalf("ListEndpoints = %+v, %v", endpoints, err)
	}

	// Two artifacts share one endpoint; replacement must not touch the other.
	rowsA := []DesiredRow{
		{RowID: "companion:gateway-1:dev-a:cam-1", SourceDevice: "cam-1", Camera: "cam-1",
			PipelineID: "pipe-a", ConclusionDeviceID: "dev-a", Enabled: true,
			ArtifactKey: "artifact-a", Status: StatusPending},
		{RowID: "companion:gateway-1:dev-a:cam-2", SourceDevice: "cam-2", Camera: "cam-2",
			PipelineID: "pipe-a", ConclusionDeviceID: "dev-a", Enabled: true,
			ArtifactKey: "artifact-a", Status: StatusPending},
	}
	if err := store.ReplaceDesired(ctx, dbTestTenant, ep.ID, "artifact-a", rowsA); err != nil {
		t.Fatalf("replace A: %v", err)
	}
	rowsB := []DesiredRow{
		{RowID: "companion:gateway-1:dev-b:cam-3", SourceDevice: "cam-3", Camera: "cam-3",
			PipelineID: "pipe-b", ConclusionDeviceID: "dev-b", Enabled: true,
			ArtifactKey: "artifact-b", Status: StatusPending},
	}
	if err := store.ReplaceDesired(ctx, dbTestTenant, ep.ID, "artifact-b", rowsB); err != nil {
		t.Fatalf("replace B: %v", err)
	}
	desired, err := store.ListDesired(ctx, dbTestTenant, ep.ID)
	if err != nil || len(desired) != 3 || desired[0].SourceDevice != "cam-1" {
		t.Fatalf("ListDesired = %+v, %v", desired, err)
	}

	// Re-replace A with one row: the old A row disappears, B stays.
	if err := store.ReplaceDesired(ctx, dbTestTenant, ep.ID, "artifact-a", rowsA[:1]); err != nil {
		t.Fatalf("re-replace A: %v", err)
	}
	desired, _ = store.ListDesired(ctx, dbTestTenant, ep.ID)
	if len(desired) != 2 {
		t.Fatalf("after re-replace rows = %d, want 2", len(desired))
	}

	// Read-back updates revision/status on existing rows only.
	if err := store.ApplyReadback(ctx, dbTestTenant, ep.ID, []ReadbackRow{
		{ID: "companion:gateway-1:dev-a:cam-1", Revision: 7, Status: StatusApplied},
		{ID: "companion:gateway-1:dev-b:cam-3", Revision: 1, Status: StatusFailed, Reason: "boom"},
	}); err != nil {
		t.Fatalf("apply readback: %v", err)
	}
	desired, _ = store.ListDesired(ctx, dbTestTenant, ep.ID)
	statuses := map[string]DesiredRow{}
	for _, row := range desired {
		statuses[row.RowID] = row
	}
	if statuses["companion:gateway-1:dev-a:cam-1"].Revision != 7 ||
		statuses["companion:gateway-1:dev-a:cam-1"].Status != StatusApplied {
		t.Fatalf("read-back not persisted: %+v", statuses)
	}
	if statuses["companion:gateway-1:dev-b:cam-3"].Reason != "boom" {
		t.Fatalf("failed reason not persisted: %+v", statuses["companion:gateway-1:dev-b:cam-3"])
	}

	// Deleting A leaves exactly B's row and reports the remaining count.
	endpointID, remaining, err := store.DeleteDesiredArtifact(ctx, dbTestTenant, "artifact-a")
	if err != nil || endpointID != ep.ID || remaining != 1 {
		t.Fatalf("delete A = (%q, %d, %v)", endpointID, remaining, err)
	}
	// Idempotent second delete is a no-op.
	endpointID, remaining, err = store.DeleteDesiredArtifact(ctx, dbTestTenant, "artifact-a")
	if err != nil || endpointID != "" || remaining != 0 {
		t.Fatalf("repeat delete = (%q, %d, %v)", endpointID, remaining, err)
	}

	// Endpoint deletion cascades the remaining desired rows.
	if err := store.DeleteEndpoint(ctx, dbTestTenant, ep.ID); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM companion_desired_subscription WHERE tenant_id=$1`,
		dbTestTenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("cascade left %d desired rows", n)
	}
	if err := store.DeleteEndpoint(ctx, dbTestTenant, ep.ID); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("repeat delete err = %v, want ErrStoreNotFound", err)
	}
}
