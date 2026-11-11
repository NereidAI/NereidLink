// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// bootstrap_db_test.go verifies first-run administrator provisioning against
// an opt-in PostgreSQL database: status reports required while the hash is
// empty, CompleteBootstrap sets it exactly once (a racing second call fails),
// and login with an unset password returns ErrPasswordNotSet without
// incrementing the lockout counter.
//
// The test is non-destructive: it snapshots the existing admin row's
// password/flags, forces the empty-hash state, and restores everything on
// cleanup. On a freshly migrated database that row is the migration-0049
// deterministic admin; on an older seeded database it is the legacy admin.

package identity

import (
	"context"
	"testing"
)

// bootstrapFixture guarantees exactly one first-run admin in the empty-hash
// state and restores the original row when the test ends.
type bootstrapFixture struct {
	id              string
	origHash        *string
	origMustChange  bool
	origDisabled    bool
	origFailedCount int
	created         bool
}

func setupBootstrap(t *testing.T, svc *Service) string {
	t.Helper()
	ctx := context.Background()

	rows, err := svc.pool.Query(ctx,
		`SELECT id::text, password_hash, must_change_password, disabled,
		        failed_login_count
		 FROM app_user WHERE email = $1 AND tenant_id IS NULL
		 ORDER BY created_at`, BootstrapAdminEmail)
	if err != nil {
		t.Fatalf("read admin rows: %v", err)
	}
	type adminRow struct {
		id                   string
		hash                 string
		mustChange, disabled bool
		failed               int
	}
	var admins []adminRow
	for rows.Next() {
		var a adminRow
		if err := rows.Scan(&a.id, &a.hash, &a.mustChange, &a.disabled, &a.failed); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		admins = append(admins, a)
	}
	rows.Close()
	if len(admins) > 1 {
		t.Skip("database has multiple platform admins with the bootstrap email; run on a canonical database")
	}

	f := &bootstrapFixture{}
	if len(admins) == 0 {
		f.id = "00000000-0000-0000-0000-0000000000a1"
		f.created = true
		if _, err := svc.pool.Exec(ctx,
			`INSERT INTO app_user (id, tenant_id, email, display_name,
			    password_hash, is_platform_admin, email_verified, must_change_password)
			 VALUES ($1, NULL, $2, 'Platform Admin', '', TRUE, TRUE, TRUE)`,
			f.id, BootstrapAdminEmail); err != nil {
			t.Fatalf("insert bootstrap admin: %v", err)
		}
	} else {
		a := admins[0]
		f.id = a.id
		h := a.hash
		f.origHash = &h
		f.origMustChange = a.mustChange
		f.origDisabled = a.disabled
		f.origFailedCount = a.failed
	}

	if _, err := svc.pool.Exec(ctx,
		`UPDATE app_user
		 SET password_hash = '', must_change_password = TRUE,
		     disabled = FALSE, failed_login_count = 0, locked_until = NULL
		 WHERE id = $1`, f.id); err != nil {
		t.Fatalf("force bootstrap state: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		if f.created {
			_, _ = svc.pool.Exec(c, `DELETE FROM app_user WHERE id = $1`, f.id)
			return
		}
		_, _ = svc.pool.Exec(c,
			`UPDATE app_user
			 SET password_hash = $2, must_change_password = $3,
			     disabled = $4, failed_login_count = $5
			 WHERE id = $1`,
			f.id, *f.origHash, f.origMustChange, f.origDisabled, f.origFailedCount)
	})
	return f.id
}

func TestBootstrapStatusAndComplete(t *testing.T) {
	pool := mfaTestPool(t)
	ctx := context.Background()
	svc, err := NewService(pool, "bootstrap-test-signing-secret-0123456789abcdef", DefaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	setupBootstrapID := setupBootstrap(t, svc)

	status, err := svc.BootstrapStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Required || status.Email != BootstrapAdminEmail {
		t.Fatalf("expected bootstrap required for %s, got %+v", BootstrapAdminEmail, status)
	}

	// Login while the password is unset must be a dedicated error, not a
	// generic credential failure, and must not increment the failure count.
	if _, err := svc.Login(ctx, BootstrapAdminEmail, "whatever", "127.0.0.1", "test"); err != ErrPasswordNotSet {
		t.Fatalf("want ErrPasswordNotSet, got %v", err)
	}
	var failures int
	if err := pool.QueryRow(ctx,
		`SELECT failed_login_count FROM app_user WHERE id = $1`,
		setupBootstrapID).Scan(&failures); err != nil {
		t.Fatalf("read failure count: %v", err)
	}
	if failures != 0 {
		t.Fatalf("unset-password login incremented failure count to %d", failures)
	}

	const newPassword = "FirstRunStrongPass123"
	pair, err := svc.CompleteBootstrap(ctx, newPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("complete bootstrap: %v", err)
	}
	if pair.AccessToken == "" || pair.User.Email != BootstrapAdminEmail {
		t.Fatalf("unexpected token pair: %+v", pair.User)
	}

	status, err = svc.BootstrapStatus(ctx)
	if err != nil {
		t.Fatalf("status after: %v", err)
	}
	if status.Required {
		t.Fatal("bootstrap must report not-required after setup")
	}

	// The endpoint is single-use: a second completion must fail.
	if _, err := svc.CompleteBootstrap(ctx, "AnotherStrongPass456", "127.0.0.1", "test"); err != ErrBootstrapComplete {
		t.Fatalf("want ErrBootstrapComplete, got %v", err)
	}

	// The chosen password now logs in.
	result, err := svc.Login(ctx, BootstrapAdminEmail, newPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("login after bootstrap: %v", err)
	}
	if result.Pair == nil || result.Pair.AccessToken == "" {
		t.Fatalf("expected token pair, got %+v", result)
	}
}

func TestBootstrapPasswordPolicy(t *testing.T) {
	pool := mfaTestPool(t)
	svc, err := NewService(pool, "bootstrap-policy-signing-secret-0123456789", DefaultTuning())
	if err != nil {
		t.Fatal(err)
	}
	setupBootstrap(t, svc)
	if _, err := svc.CompleteBootstrap(context.Background(), "short", "ip", "test"); err == nil {
		t.Fatal("a password shorter than policy must be rejected")
	}
	// Failed policy validation must not consume the bootstrap.
	status, err := svc.BootstrapStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Required {
		t.Fatal("bootstrap should still be required after a rejected password")
	}
}
