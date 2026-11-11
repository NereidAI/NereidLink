// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_db_test.go exercises the full order lifecycle against a real
// PostgreSQL instance. It is opt-in via NL_TEST_DATABASE_URL so the
// default hermetic test run stays database-free.
//
// Flow under test: an operator opens a destructive order from the CLI
// (no application account needed), the order waits PENDING, and the
// super administrator confirms it on the web. CLI-side confirmation is
// rejected as same-channel.

package opscenter

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dbTestPool connects when NL_TEST_DATABASE_URL is configured and
// skips the test otherwise.
func dbTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// TestOrderLifecycleDB verifies create, cross-channel enforcement,
// approval, execution, rejection, cancellation and the expiry sweep.
func TestOrderLifecycleDB(t *testing.T) {
	pool := dbTestPool(t)
	ctx := context.Background()
	svc := New(pool)

	operator := CreateOrderInput{
		Action:           ActionTelemetryPurge,
		Summary:          "purge old telemetry",
		Payload:          []byte(`{"before":"2026-09-01T00:00:00Z"}`),
		InitiatorName:    "ops-alice",
		InitiatorChannel: ChannelCLI,
		TTL:              time.Hour,
	}

	// A web-channel create is rejected; orders are CLI-only.
	webAttempt := operator
	webAttempt.InitiatorChannel = ChannelWeb
	if _, err := svc.Create(ctx, webAttempt); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("web create: got %v", err)
	}

	order, err := svc.Create(ctx, operator)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if order.Status != StatusPending || len(order.ContentHash) != 64 {
		t.Fatalf("unexpected created order: %+v", order)
	}

	// Super administrator principal for web-side confirmation.
	admin := Reviewer{
		ID:      "00000000-0000-0000-0000-000000000001",
		Name:    "admin@nereidai.local",
		Channel: ChannelWeb,
	}
	// Same-channel (CLI) confirmation must be rejected.
	cliReviewer := Reviewer{
		ID:      admin.ID,
		Name:    admin.Name,
		Channel: ChannelCLI,
	}
	if _, err := svc.Approve(ctx, order.ID, cliReviewer); !errors.Is(err, ErrSameChannel) {
		t.Fatalf("cli confirm: got %v want ErrSameChannel", err)
	}

	// No executors are configured in this test, so approval leaves the
	// order APPROVED for later execution.
	approved, err := svc.Approve(ctx, order.ID, admin)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != StatusApproved {
		t.Fatalf("got status %s", approved.Status)
	}
	lateAdmin := admin
	lateAdmin.Reason = "late rejection"
	if _, err := svc.Reject(ctx, order.ID, lateAdmin); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-review: got %v want ErrNotPending", err)
	}
	executed, err := svc.MarkExecuted(ctx, order.ID, []byte(`{"deleted":12}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if executed.Status != StatusExecuted {
		t.Fatalf("got status %s", executed.Status)
	}

	// Reject flow with a fresh order; reason is required.
	order2, err := svc.Create(ctx, operator)
	if err != nil {
		t.Fatalf("create order2: %v", err)
	}
	if _, err := svc.Reject(ctx, order2.ID, admin); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reject without reason: got %v", err)
	}
	rejected, err := svc.Reject(ctx, order2.ID, Reviewer{
		ID:      admin.ID,
		Name:    admin.Name,
		Channel: ChannelWeb,
		Reason:  "cutoff is wrong",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("got status %s", rejected.Status)
	}

	// Cancel flow: a different operator cannot cancel.
	order3, err := svc.Create(ctx, operator)
	if err != nil {
		t.Fatalf("create order3: %v", err)
	}
	if _, err := svc.Cancel(ctx, order3.ID, "ops-bob"); !errors.Is(err, ErrNotInitiator) {
		t.Fatalf("cancel by other: got %v", err)
	}
	cancelled, err := svc.Cancel(ctx, order3.ID, "ops-alice")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("got status %s", cancelled.Status)
	}

	// Expiry sweep: insert an already-expired pending order directly.
	var expiredID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO ops_order
		   (action, payload, content_hash, initiator_name,
		    initiator_channel, status, expires_at)
		 VALUES ($1, '{}', 'x', $2, $3, $4, now() - interval '1 hour')
		 RETURNING id::text`,
		ActionQuarantineRelease, "ops-alice", ChannelCLI,
		StatusPending).Scan(&expiredID); err != nil {
		t.Fatalf("insert stale: %v", err)
	}
	count, err := svc.ExpireStale(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if count < 1 {
		t.Fatalf("sweep expired %d rows", count)
	}
	stale, err := svc.Get(ctx, expiredID)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if stale.Status != StatusExpired {
		t.Fatalf("got status %s", stale.Status)
	}
	if _, err := svc.Approve(ctx, expiredID, admin); !errors.Is(err, ErrOrderExpired) {
		t.Fatalf("approve expired: got %v", err)
	}
}
