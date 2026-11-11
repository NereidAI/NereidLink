// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// runner_db_test.go covers escalation firing and cancellation against an
// opt-in PostgreSQL database. External delivery and inbox recording are
// replaced by fakes so the test asserts firing state without SMTP/NATS.

package escalation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// escalationTestPool connects when NL_TEST_DATABASE_URL is configured.
func escalationTestPool(t *testing.T) *pgxpool.Pool {
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

// fakeSender records delivered escalation messages.
type fakeSender struct{ sent []SendInput }

// Send records one delivery; it never fails.
func (f *fakeSender) Send(_ context.Context, in SendInput) error {
	f.sent = append(f.sent, in)
	return nil
}

// fakeInbox records in-app notifications.
type fakeInbox struct{ recorded []InboxRecord }

// Record records one notification.
func (f *fakeInbox) Record(_ context.Context, in InboxRecord) error {
	f.recorded = append(f.recorded, in)
	return nil
}

// seedEscalationFixture inserts disposable tenant and device rows and
// returns their ids plus a cleanup function.
func seedEscalationFixture(t *testing.T, pool *pgxpool.Pool) (string, string, func()) {
	t.Helper()
	ctx := context.Background()
	tenantID := "00000000-0000-0000-0000-00000000e501"
	deviceID := "00000000-0000-0000-0000-00000000ed01"

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'escalation-test')
		 ON CONFLICT (id) DO NOTHING`, tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state)
		 VALUES ($1, $2, 'escalation-device', 'ACTIVE')
		 ON CONFLICT (id) DO NOTHING`, deviceID, tenantID); err != nil {
		t.Fatalf("insert device: %v", err)
	}

	cleanup := func() {
		background := context.Background()
		if _, err := pool.Exec(background,
			`DELETE FROM device WHERE id = $1`, deviceID); err != nil {
			t.Logf("cleanup device: %v", err)
		}
		if _, err := pool.Exec(background,
			`DELETE FROM tenant WHERE id = $1`, tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	}
	return tenantID, deviceID, cleanup
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// insertAlarmWithChain inserts a live alarm bound to chainID.
func insertAlarmWithChain(t *testing.T, pool *pgxpool.Pool, tenantID, deviceID, chainID, state string, startedAt time.Time) string {
	t.Helper()
	alarmID := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO alarm
		   (id, tenant_id, device_id, type, severity, state, dedupe_key,
		    started_at, escalation_chain_id)
		 VALUES ($1,$2,$3,'test','CRITICAL',$4,$5,$6,$7)`,
		alarmID, tenantID, deviceID, state, alarmID, startedAt, chainID); err != nil {
		t.Fatalf("insert alarm: %v", err)
	}
	return alarmID
}

// TestRunnerFiresDueStep verifies materialization and delivery of a
// zero-delay step exactly once.
func TestRunnerFiresDueStep(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, deviceID, cleanupFixture := seedEscalationFixture(t, pool)

	chain, err := svc.CreateChain(ctx, tenantID, SaveInput{
		Name:    "fire-chain-" + uuid.NewString(),
		Enabled: boolPtr(true),
		Steps: []StepInput{{
			DelaySeconds: 0,
			TargetRole:   "ENGINEER",
			StopOnAck:    true,
			StopOnClear:  true,
		}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}

	alarmID := insertAlarmWithChain(t, pool, tenantID, deviceID, chain.ID, "ACTIVE", time.Now().Add(-time.Minute))

	sender := &fakeSender{}
	inbox := &fakeInbox{}
	runner := NewRunner(pool, sender, inbox, time.Second)

	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if len(sender.sent) != 1 || len(inbox.recorded) != 1 {
		t.Fatalf("delivery = %d, inbox = %d, want 1 each", len(sender.sent), len(inbox.recorded))
	}
	if sender.sent[0].Severity != "CRITICAL" {
		t.Fatalf("severity = %q", sender.sent[0].Severity)
	}

	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("step fired %d times, want exactly once", len(sender.sent))
	}

	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM alarm_escalation_state WHERE alarm_id = $1`,
		alarmID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != StateFired {
		t.Fatalf("state = %q, want FIRED", state)
	}

	cleanupChain(t, pool, tenantID, chain.ID)
	cleanupFixture()
}

// TestRunnerCancelsAckedStep verifies a pending step with stop_on_ack is
// canceled instead of fired once the alarm is acknowledged.
func TestRunnerCancelsAckedStep(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, deviceID, cleanupFixture := seedEscalationFixture(t, pool)

	chain, err := svc.CreateChain(ctx, tenantID, SaveInput{
		Name:    "ack-chain-" + uuid.NewString(),
		Enabled: boolPtr(true),
		Steps: []StepInput{{
			DelaySeconds: 3600,
			TargetRole:   "TENANT_ADMIN",
			StopOnAck:    true,
			StopOnClear:  true,
		}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}

	alarmID := insertAlarmWithChain(t, pool, tenantID, deviceID, chain.ID, "ACK", time.Now().Add(-time.Minute))

	sender := &fakeSender{}
	inbox := &fakeInbox{}
	runner := NewRunner(pool, sender, inbox, time.Second)

	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(sender.sent) != 0 || len(inbox.recorded) != 0 {
		t.Fatalf("ack alarm escalated: deliveries=%d inbox=%d", len(sender.sent), len(inbox.recorded))
	}

	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM alarm_escalation_state WHERE alarm_id = $1`,
		alarmID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != StateCanceled {
		t.Fatalf("state = %q, want CANCELED", state)
	}

	cleanupChain(t, pool, tenantID, chain.ID)
	cleanupFixture()
}

// TestRunnerRepeatsRemindersUntilAck verifies ADR-0019 repeated
// reminders: an ACTIVE alarm bound to a chain with a repeat interval is
// reminded on cadence, and reminders stop once the alarm is acked.
func TestRunnerRepeatsRemindersUntilAck(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, deviceID, cleanupFixture := seedEscalationFixture(t, pool)

	chain, err := svc.CreateChain(ctx, tenantID, SaveInput{
		Name:                  "reminder-chain-" + uuid.NewString(),
		Enabled:               boolPtr(true),
		RepeatIntervalSeconds: 300,
		RepeatTargetRole:      "TENANT_ADMIN",
		Steps: []StepInput{{
			DelaySeconds: 0,
			TargetRole:   "ENGINEER",
		}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}

	// Started 7 minutes ago: first reminder (started+5m) is due, the
	// following one (started+10m) is still in the future.
	alarmID := insertAlarmWithChain(t, pool, tenantID, deviceID, chain.ID,
		"ACTIVE", time.Now().Add(-7*time.Minute))

	sender := &fakeSender{}
	inbox := &fakeInbox{}
	runner := NewRunner(pool, sender, inbox, time.Second)

	countReminders := func() int {
		n := 0
		for _, m := range sender.sent {
			if strings.Contains(m.Subject, "REMINDER") {
				n++
			}
		}
		return n
	}

	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := countReminders(); got != 1 {
		t.Fatalf("reminders after first sweep = %d, want 1", got)
	}

	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := countReminders(); got != 1 {
		t.Fatalf("reminders after second sweep = %d, want 1 (cursor advanced to future)", got)
	}

	// Acknowledge: the reminder cursor must be removed and no more sends.
	if _, err := pool.Exec(ctx, `UPDATE alarm SET state='ACK' WHERE id=$1`, alarmID); err != nil {
		t.Fatalf("ack alarm: %v", err)
	}
	if err := runner.sweep(ctx); err != nil {
		t.Fatalf("post-ack sweep: %v", err)
	}
	if got := countReminders(); got != 1 {
		t.Fatalf("reminders after ack = %d, want 1", got)
	}
	var cursors int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM alarm_reminder_state WHERE alarm_id=$1`, alarmID).Scan(&cursors); err != nil {
		t.Fatalf("count cursors: %v", err)
	}
	if cursors != 0 {
		t.Fatalf("reminder cursors after ack = %d, want 0", cursors)
	}

	cleanupChain(t, pool, tenantID, chain.ID)
	cleanupFixture()
}

// TestReminderConfigRequiresRole verifies a positive repeat interval
// without a valid target role is rejected.
func TestReminderConfigRequiresRole(t *testing.T) {
	pool := escalationTestPool(t)
	svc := NewService(pool)
	ctx := context.Background()
	tenantID, _, cleanupFixture := seedEscalationFixture(t, pool)
	defer cleanupFixture()

	_, err := svc.CreateChain(ctx, tenantID, SaveInput{
		Name:                  "bad-reminder-" + uuid.NewString(),
		RepeatIntervalSeconds: 300,
		Steps: []StepInput{{
			DelaySeconds: 0,
			TargetRole:   "ENGINEER",
		}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("repeat without role: err = %v, want ErrInvalid", err)
	}
}

// cleanupChain deletes a chain and its derived state rows.
func cleanupChain(t *testing.T, pool *pgxpool.Pool, tenantID, chainID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`DELETE FROM alarm_escalation_state
		 WHERE alarm_id IN (SELECT id FROM alarm WHERE escalation_chain_id = $1)`,
		chainID); err != nil {
		t.Logf("cleanup state: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM alarm WHERE escalation_chain_id = $1`, chainID); err != nil {
		t.Logf("cleanup alarms: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM escalation_chain WHERE id = $1 AND tenant_id = $2`,
		chainID, tenantID); err != nil {
		t.Logf("cleanup chain: %v", err)
	}
}
