// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// candidate_db_test.go verifies migration 0046 discovery_candidate: pending
// idempotency (one PENDING per gateway/local_id), re-insertion after a
// terminal decision, listing/filtering, decision CAS, and the approval_mode
// policy round-trip.

package discovery

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const candidateTestTenant = "00000000-0000-0000-0000-00000000d806"

func newCandidateDB(t *testing.T) (*CandidateStore, *PolicyStore, *pgxpool.Pool) {
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'dc-test') ON CONFLICT DO NOTHING`,
		candidateTestTenant); err != nil {
		t.Fatal(err)
	}
	return NewCandidateStore(pool), NewPolicyStore(pool), pool
}

func resetCandidateFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM discovery_candidate WHERE tenant_id=$1`, candidateTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM discovery_policy WHERE tenant_id=$1`, candidateTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM device WHERE tenant_id=$1`, candidateTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, candidateTestTenant)
}

// ensureCandidateTenant (re)creates the tenant after a reset wiped it.
func ensureCandidateTenant(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenant (id,name) VALUES ($1,'dc-test') ON CONFLICT DO NOTHING`,
		candidateTestTenant); err != nil {
		t.Fatal(err)
	}
}

func mkCandidateGateway(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway, state)
		 VALUES (gen_random_uuid(),$1,$2,TRUE,'ACTIVE') RETURNING id::text`,
		candidateTestTenant, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCandidateStorePendingLifecycle(t *testing.T) {
	store, _, pool := newCandidateDB(t)
	resetCandidateFixture(pool)
	ensureCandidateTenant(t, pool)
	t.Cleanup(func() { resetCandidateFixture(pool) })
	ctx := context.Background()

	gw := mkCandidateGateway(t, pool, "dc-gw")

	// First insert creates PENDING.
	rec, err := store.PendingInsert(ctx, NewCandidate{
		TenantID: candidateTestTenant, GatewayID: gw,
		LocalID: "unit-1", CandidateName: "dc-gw/unit-1", SourceType: "modbus",
	})
	if err != nil {
		t.Fatalf("PendingInsert: %v", err)
	}
	if rec.State != StatePending || rec.SourceType != "modbus" {
		t.Fatalf("unexpected record: %+v", rec)
	}

	// Duplicate live request collides on the partial unique index.
	if _, err := store.PendingInsert(ctx, NewCandidate{
		TenantID: candidateTestTenant, GatewayID: gw, LocalID: "unit-1",
	}); !errors.Is(err, ErrPendingExists) {
		t.Fatalf("duplicate want ErrPendingExists, got %v", err)
	}

	// A second distinct local id is allowed.
	rec2, err := store.PendingInsert(ctx, NewCandidate{
		TenantID: candidateTestTenant, GatewayID: gw, LocalID: "unit-2",
	})
	if err != nil {
		t.Fatalf("PendingInsert unit-2: %v", err)
	}

	// List filter state=PENDING for this gateway.
	items, total, err := store.ListCandidates(ctx, candidateTestTenant, StatePending, gw, 50, 0)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list pending: total=%d len=%d err=%v", total, len(items), err)
	}

	// Approve unit-1 against a real created sub-device (device_id FK).
	var devID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, gateway_id, is_gateway, state)
		 VALUES (gen_random_uuid(),$1,'dc-gw/unit-1',$2,FALSE,'ACTIVE') RETURNING id::text`,
		candidateTestTenant, gw).Scan(&devID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkApproved(ctx, candidateTestTenant, rec.ID, devID, "user-1", ""); err != nil {
		t.Fatalf("MarkApproved: %v", err)
	}
	got, err := store.GetByID(ctx, candidateTestTenant, rec.ID)
	if err != nil || got.State != StateApproved || got.DeviceID != devID || got.DecidedBy != "user-1" {
		t.Fatalf("approved record: %+v err=%v", got, err)
	}
	if err := store.MarkApproved(ctx, candidateTestTenant, rec.ID, devID, "u", ""); !errors.Is(err, ErrCandidateNotPending) {
		t.Fatalf("re-decide want ErrCandidateNotPending, got %v", err)
	}

	// Reject unit-2, then re-discover unit-2: a fresh PENDING is allowed
	// because the partial index only covers live rows.
	if err := store.MarkRejected(ctx, candidateTestTenant, rec2.ID, "user-1", "nope"); err != nil {
		t.Fatalf("MarkRejected: %v", err)
	}
	if _, err := store.PendingInsert(ctx, NewCandidate{
		TenantID: candidateTestTenant, GatewayID: gw, LocalID: "unit-2",
	}); err != nil {
		t.Fatalf("re-discover after reject should insert, got %v", err)
	}

	// Unknown id.
	if _, err := store.GetByID(ctx, candidateTestTenant, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("missing want ErrCandidateNotFound, got %v", err)
	}
}

// TestApprovalModePolicyRoundTrip verifies the 0046 policy column persists and
// rejects an invalid mode.
func TestApprovalModePolicyRoundTrip(t *testing.T) {
	_, policies, pool := newCandidateDB(t)
	resetCandidateFixture(pool)
	ensureCandidateTenant(t, pool)
	t.Cleanup(func() { resetCandidateFixture(pool) })
	ctx := context.Background()

	gw := mkCandidateGateway(t, pool, "dc-gw2")
	review := ApprovalReview
	rec, err := policies.SetGateway(ctx, candidateTestTenant, gw, Override{ApprovalMode: &review})
	if err != nil {
		t.Fatalf("SetGateway review: %v", err)
	}
	if rec.ApprovalMode == nil || *rec.ApprovalMode != ApprovalReview {
		t.Fatalf("approval mode not persisted: %+v", rec.ApprovalMode)
	}
	bad := "bogus"
	if _, err := policies.SetGateway(ctx, candidateTestTenant, gw, Override{ApprovalMode: &bad}); !errors.Is(err, ErrInvalidApprovalMode) {
		t.Fatalf("bad mode want ErrInvalidApprovalMode, got %v", err)
	}
}
