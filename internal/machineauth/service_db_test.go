// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_db_test.go exercises service-account lifecycle, overlap
// rotation and the client_credentials grant against a real PostgreSQL
// database. It is an integration test gated on NEREIDLINK_TEST_DSN.
//
// Every test creates its own tenant and removes it on cleanup: the
// foreign key cascade deletes the service accounts and secrets, so no
// unqualified DELETE can ever touch another test's or a live
// deployment's rows.

package machineauth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// stubIssuer records the last grant parameters and returns a fixed
// token, avoiding the identity signer and its session machinery.
type stubIssuer struct {
	subject string
	tenant  string
	client  string
	scopes  []string
	calls   int
}

func (s *stubIssuer) IssueMachineAccessToken(subject, tenantID, clientID string,
	scopes []string) (string, time.Time, error) {
	s.subject, s.tenant, s.client, s.scopes, s.calls =
		subject, tenantID, clientID, scopes, s.calls+1
	return "machine-token", time.Now().Add(30 * time.Minute), nil
}

// testService provisions a pool, isolated tenant and service.
func testService(t *testing.T) (*Service, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping machineauth integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.service_account') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		t.Skip("service_account missing; run migration 0037")
	}

	tenantID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'machineauth-test')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id = $1`, tenantID)
	})

	svc := New(pool, &stubIssuer{})
	return svc, pool, tenantID
}

// TestCreateAndIssue covers the happy path and verifies token claims
// reflect the account scopes.
func TestCreateAndIssue(t *testing.T) {
	svc, _, tenantID := testService(t)
	ctx := context.Background()

	result, err := svc.Create(ctx, tenantID, CreateInput{
		Name:   "ci-publisher",
		Scopes: []string{rbac.PointDeviceRead, rbac.PointStats},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result.Credentials.ClientID == "" || result.Credentials.ClientSecret == "" {
		t.Fatal("credentials must be returned once")
	}

	granted, err := svc.IssueToken(ctx,
		result.Credentials.ClientID, result.Credentials.ClientSecret, "10.0.0.1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if granted.AccessToken != "machine-token" {
		t.Fatalf("unexpected token %q", granted.AccessToken)
	}
	issuer := svc.issuer.(*stubIssuer)
	if issuer.subject != result.Account.ID || issuer.tenant != tenantID {
		t.Fatalf("issuer claims wrong: sub=%s tenant=%s", issuer.subject, issuer.tenant)
	}
	if len(issuer.scopes) != 2 {
		t.Fatalf("scopes not propagated: %v", issuer.scopes)
	}
}

// TestRotateSecretOverlap verifies the old secret authenticates while
// rotating and is retired on the second rotation, keeping at most two
// stored secrets.
func TestRotateSecretOverlap(t *testing.T) {
	svc, pool, tenantID := testService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, tenantID, CreateInput{Name: "rot"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	clientID := created.Credentials.ClientID
	firstSecret := created.Credentials.ClientSecret

	rot1, err := svc.RotateSecret(ctx, tenantID, created.Account.ID, nil)
	if err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	// Both old and new work during overlap.
	if _, err := svc.IssueToken(ctx, clientID, firstSecret, "ip"); err != nil {
		t.Fatalf("old primary should work during overlap: %v", err)
	}
	if _, err := svc.IssueToken(ctx, clientID, rot1.NewSecret, "ip"); err != nil {
		t.Fatalf("new primary should work: %v", err)
	}

	rot2, err := svc.RotateSecret(ctx, tenantID, created.Account.ID, nil)
	if err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	// The first secret must now be retired; exactly two rows remain.
	if _, err := svc.IssueToken(ctx, clientID, firstSecret, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("oldest secret should be retired, got %v", err)
	}
	if _, err := svc.IssueToken(ctx, clientID, rot1.NewSecret, "ip"); err != nil {
		t.Fatalf("second secret should now be rotating: %v", err)
	}
	if _, err := svc.IssueToken(ctx, clientID, rot2.NewSecret, "ip"); err != nil {
		t.Fatalf("newest secret should be primary: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM service_account_secret s
		 JOIN service_account a ON a.id = s.service_account_id
		 WHERE a.client_id = $1`, clientID).Scan(&n); err != nil {
		t.Fatalf("count secrets: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected at most 2 stored secrets, got %d", n)
	}
}

// TestIssueRejectsUnknownDisabledWrong covers the failure states.
func TestIssueRejectsUnknownDisabledWrong(t *testing.T) {
	svc, _, tenantID := testService(t)
	ctx := context.Background()

	if _, err := svc.IssueToken(ctx, "sa_doesnotexist", "x", "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown client must be invalid credentials, got %v", err)
	}

	created, err := svc.Create(ctx, tenantID, CreateInput{Name: "d"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.IssueToken(ctx, created.Credentials.ClientID, "wrong", "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong secret, got %v", err)
	}

	if _, err := svc.SetEnabled(ctx, tenantID, created.Account.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := svc.IssueToken(ctx, created.Credentials.ClientID,
		created.Credentials.ClientSecret, "ip"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled account, got %v", err)
	}
}

// TestIssueLockout verifies repeated failures lock the client, not a
// client+IP pair: an ephemeral peer port or a rotating X-Forwarded-For
// must not evade the guard.
func TestIssueLockout(t *testing.T) {
	svc, _, tenantID := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenantID, CreateInput{Name: "lock"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var sawLock bool
	for i := 0; i < lockoutThreshold+2; i++ {
		_, err := svc.IssueToken(ctx, created.Credentials.ClientID, "wrong",
			"9.9.9."+string(rune('0'+i%10)))
		switch {
		case errors.Is(err, ErrLocked):
			sawLock = true
		case errors.Is(err, ErrInvalidCredentials):
			// expected until the threshold
		default:
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if !sawLock {
		t.Fatal("expected lockout after repeated failures")
	}
	// The lock is keyed on client id, so even the correct secret from a
	// different peer must not bypass it within the window.
	if _, err := svc.IssueToken(ctx, created.Credentials.ClientID,
		created.Credentials.ClientSecret, "8.8.8.8"); !errors.Is(err, ErrLocked) {
		t.Fatalf("client lockout must block from any peer, got %v", err)
	}
}

// TestUpdateScopes verifies scope changes are persisted and invalid
// scopes rejected.
func TestUpdateScopes(t *testing.T) {
	svc, _, tenantID := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenantID, CreateInput{Name: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := svc.Update(ctx, tenantID, created.Account.ID,
		nil, []string{rbac.PointAlarmRead}, true)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.Scopes) != 1 || updated.Scopes[0] != rbac.PointAlarmRead {
		t.Fatalf("scopes not updated: %v", updated.Scopes)
	}
	if _, err := svc.Update(ctx, tenantID, created.Account.ID,
		nil, []string{rbac.PointServiceAccountWrite}, true); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("identity-admin scope must be rejected, got %v", err)
	}
}

// TestListGetDelete verifies management reads and tenant scoping.
func TestListGetDelete(t *testing.T) {
	svc, _, tenantID := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenantID, CreateInput{Name: "l"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Get(ctx, tenantID, created.Account.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientID != created.Credentials.ClientID {
		t.Fatal("get returned wrong account")
	}
	listed, err := svc.List(ctx, tenantID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v len=%d", err, len(listed))
	}
	// Cross-tenant lookup must miss.
	if _, err := svc.Get(ctx, uuid.NewString(), created.Account.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get, got %v", err)
	}
	if err := svc.Delete(ctx, tenantID, created.Account.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, tenantID, created.Account.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted account, got %v", err)
	}
}

// TestCreateRejectsBadScope ensures the service enforces the grantable
// set independently of the database CHECK.
func TestCreateRejectsBadScope(t *testing.T) {
	svc, _, tenantID := testService(t)
	_, err := svc.Create(context.Background(), tenantID, CreateInput{
		Name:   "bad",
		Scopes: []string{"root:all"},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}
