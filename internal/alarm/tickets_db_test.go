// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// tickets_db_test.go covers alarm assignment and comments against an opt-in
// PostgreSQL database. These are authorization-sensitive tenant-boundary
// operations, so the test uses disposable rows rather than depending on seeded
// production users or alarms.

package alarm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ticketsTestPool connects when NL_TEST_DATABASE_URL is configured.
func ticketsTestPool(t *testing.T) *pgxpool.Pool {
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

// seedTicketFixture inserts disposable tenant, user, device and alarm rows.
func seedTicketFixture(t *testing.T, pool *pgxpool.Pool) (string, string, string, string) {
	t.Helper()
	ctx := context.Background()

	tenantID := "00000000-0000-0000-0000-00000000aa01"
	userID := "00000000-0000-0000-0000-00000000a501"
	deviceID := "00000000-0000-0000-0000-00000000de01"
	alarmID := "00000000-0000-0000-0000-00000000a101"

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'alarm-ticket-test')
		 ON CONFLICT (id) DO NOTHING`, tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user
		   (id, tenant_id, email, display_name, email_verified)
		 VALUES ($1, $2, 'alarm-ticket@example.test', 'Ticket User', true)
		 ON CONFLICT (id) DO UPDATE
		 SET tenant_id = EXCLUDED.tenant_id,
		     email = EXCLUDED.email,
		     display_name = EXCLUDED.display_name,
		     disabled = false`, userID, tenantID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device
		   (id, tenant_id, canonical_name, state)
		 VALUES ($1, $2, 'alarm-ticket-device', 'ACTIVE')
		 ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state`,
		deviceID, tenantID); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO alarm
		   (id, tenant_id, device_id, type, severity, state, dedupe_key)
		 VALUES ($1, $2, $3, 'test', 'WARNING', 'ACTIVE', 'test-dedupe')
		 ON CONFLICT (id) DO NOTHING`,
		alarmID, tenantID, deviceID); err != nil {
		t.Fatalf("insert alarm: %v", err)
	}

	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM alarm WHERE id = $1`, alarmID); err != nil {
			t.Logf("cleanup alarm: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM device WHERE id = $1`, deviceID); err != nil {
			t.Logf("cleanup device: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM app_user WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM tenant WHERE id = $1`, tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})

	return tenantID, userID, deviceID, alarmID
}

// TestAssignAlarm verifies assignment and clearing.
func TestAssignAlarm(t *testing.T) {
	pool := ticketsTestPool(t)
	tenantID, userID, _, alarmID := seedTicketFixture(t, pool)
	svc := NewService(pool, nil)

	if err := svc.AssignAlarm(context.Background(), tenantID, alarmID, userID); err != nil {
		t.Fatalf("assign alarm: %v", err)
	}
	var assignee *string
	if err := pool.QueryRow(context.Background(),
		`SELECT assignee_id::text FROM alarm WHERE id = $1`, alarmID).Scan(&assignee); err != nil {
		t.Fatalf("read assignee: %v", err)
	}
	if assignee == nil || *assignee != userID {
		t.Fatalf("assignee = %v", assignee)
	}

	if err := svc.AssignAlarm(context.Background(), tenantID, alarmID, ""); err != nil {
		t.Fatalf("clear assignment: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT assignee_id::text FROM alarm WHERE id = $1`, alarmID).Scan(&assignee); err != nil {
		t.Fatalf("read cleared assignee: %v", err)
	}
	if assignee != nil {
		t.Fatalf("assignee = %v, want null", *assignee)
	}
}

// TestAssignAlarmRejectsForeignUser verifies tenant membership is enforced.
func TestAssignAlarmRejectsForeignUser(t *testing.T) {
	pool := ticketsTestPool(t)
	tenantID, _, _, alarmID := seedTicketFixture(t, pool)
	svc := NewService(pool, nil)

	err := svc.AssignAlarm(context.Background(), tenantID, alarmID,
		"00000000-0000-0000-0000-00000000a599")
	if !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("error = %v, want ErrInvalidAssignee", err)
	}
}

// TestAlarmComments verifies append and list ordering.
func TestAlarmComments(t *testing.T) {
	pool := ticketsTestPool(t)
	tenantID, userID, _, alarmID := seedTicketFixture(t, pool)
	svc := NewService(pool, nil)
	ctx := context.Background()

	comment, err := svc.AddComment(ctx, tenantID, alarmID, userID, "Investigating now")
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if comment.AuthorName != "Ticket User" || comment.Body != "Investigating now" {
		t.Fatalf("comment = %+v", comment)
	}

	comments, err := svc.ListComments(ctx, tenantID, alarmID)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].CommentID != comment.CommentID {
		t.Fatalf("comments = %+v", comments)
	}
}

// TestAddCommentValidation verifies blank comments are rejected.
func TestAddCommentValidation(t *testing.T) {
	pool := ticketsTestPool(t)
	tenantID, userID, _, alarmID := seedTicketFixture(t, pool)
	svc := NewService(pool, nil)

	if _, err := svc.AddComment(context.Background(), tenantID, alarmID, userID, "   "); err == nil {
		t.Fatal("blank comment accepted")
	}
}

// TestAddCommentMissingAlarm verifies a comment cannot target an absent alarm.
func TestAddCommentMissingAlarm(t *testing.T) {
	pool := ticketsTestPool(t)
	tenantID, userID, _, _ := seedTicketFixture(t, pool)
	svc := NewService(pool, nil)

	_, err := svc.AddComment(context.Background(), tenantID,
		"00000000-0000-0000-0000-00000000a199", userID, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
