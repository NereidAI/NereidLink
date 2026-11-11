// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_db_test.go exercises user API-key lifecycle and authentication
// against PostgreSQL. It is gated on NEREIDLINK_TEST_DSN and skipped in the
// database-free unit-test pipeline.
package userapikey

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/rbac"
)

type stubAuditor struct {
	events []audit.Input
}

func (s *stubAuditor) Append(_ context.Context, input audit.Input) (audit.Event, error) {
	s.events = append(s.events, input)
	return audit.Event{}, nil
}

type apiKeyFixture struct {
	svc      *Service
	pool     *pgxpool.Pool
	tenantID string
	userID   string
	auditor  *stubAuditor
}

func testService(t *testing.T) apiKeyFixture {
	t.Helper()
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping user API-key integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.api_key') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		t.Skip("api_key missing; run migration 0092")
	}

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name, status) VALUES ($1, 'apikey-test', 'ACTIVE')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, display_name)
		 VALUES ($1, $2, $3, 'API Key Test')`,
		userID, tenantID, "apikey-"+userID+"@example.test"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant_membership (id, user_id, tenant_id, scope)
		VALUES (gen_random_uuid(), $1, $2, 'TENANT')`, userID, tenantID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO membership_role (id, membership_id, role)
		SELECT gen_random_uuid(), m.id, 'ENGINEER'
		FROM tenant_membership m WHERE m.user_id = $1`, userID); err != nil {
		t.Fatalf("attach role: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM app_user WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM tenant WHERE id = $1`, tenantID)
	})

	auditor := &stubAuditor{}
	svc := New(pool)
	svc.SetAuditor(auditor)
	svc.SetPermissionReader(rbac.NewResolver(pool))
	return apiKeyFixture{
		svc: svc, pool: pool, tenantID: tenantID, userID: userID, auditor: auditor,
	}
}

func hasAuditAction(events []audit.Input, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func hasAuditEventType(events []audit.Input, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func (f apiKeyFixture) setRole(t *testing.T, role string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE membership_role SET role = $3
		WHERE membership_id = (
			SELECT id FROM tenant_membership WHERE user_id = $1 AND tenant_id = $2
		)`, f.userID, f.tenantID, role); err != nil {
		t.Fatalf("set role: %v", err)
	}
}

func TestCreateAuthenticateAndListAPIKey(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()

	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{
		Name:   "integration",
		Scopes: []string{rbac.PointDeviceRead},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result.Plaintext == "" || result.Key.KeyPrefix == "" {
		t.Fatal("plaintext and stored prefix must be populated")
	}
	if result.Key.KeyPrefix == result.Plaintext {
		t.Fatal("list view must not contain the full plaintext key")
	}

	auth, err := fx.svc.Authenticate(ctx, result.Plaintext, "10.0.0.1")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if auth.KeyID != result.Key.ID || auth.UserID != fx.userID ||
		auth.TenantID != fx.tenantID || len(auth.Scopes) != 1 {
		t.Fatalf("unexpected auth result: %+v", auth)
	}

	keys, err := fx.svc.ListSelf(ctx, fx.userID)
	if err != nil || len(keys) != 1 || keys[0].KeyPrefix != result.Key.KeyPrefix {
		t.Fatalf("list self: %v keys=%+v", err, keys)
	}
	tenantKeys, err := fx.svc.ListTenant(ctx, fx.tenantID)
	if err != nil || len(tenantKeys) != 1 {
		t.Fatalf("list tenant: %v keys=%+v", err, tenantKeys)
	}
	if !hasAuditAction(fx.auditor.events, "create-api-key") {
		t.Fatalf("expected create audit event, got %+v", fx.auditor.events)
	}
	if hasAuditEventType(fx.auditor.events, audit.EventAuthLogin) {
		t.Fatalf("successful per-request authentication must not flood audit log: %+v", fx.auditor.events)
	}
}

func TestAuthenticateFailureMatrix(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{Name: "failure matrix"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := fx.svc.Authenticate(ctx, "nlk_bad", "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("malformed key: %v", err)
	}
	unknown := mustKey(t)
	if _, err := fx.svc.Authenticate(ctx, unknown, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown key: %v", err)
	}
	wrong := result.Plaintext
	if wrong[len(wrong)-1] == 'a' {
		wrong = wrong[:len(wrong)-1] + "b"
	} else {
		wrong = wrong[:len(wrong)-1] + "a"
	}
	if _, err := fx.svc.Authenticate(ctx, wrong, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong key must be invalid credentials: %v", err)
	}
	if !hasAuditEventType(fx.auditor.events, audit.EventAuthLoginFailed) {
		t.Fatalf("expected failed authentication audit event, got %+v", fx.auditor.events)
	}

	revoked, err := fx.svc.RevokeSelf(ctx, fx.userID, result.Key.ID)
	if err != nil || !revoked.Revoked {
		t.Fatalf("revoke: %v key=%+v", err, revoked)
	}
	if !hasAuditAction(fx.auditor.events, "revoke-api-key") {
		t.Fatalf("expected revoke audit event, got %+v", fx.auditor.events)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("revoked key must return the same invalid-credential error: %v", err)
	}
	if _, err := fx.svc.RotateSelf(ctx, fx.userID, result.Key.ID, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("revoked key rotation must be rejected: %v", err)
	}
}

func TestTenantAdministratorLifecycle(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	created, err := fx.svc.Create(ctx, fx.userID, CreateInput{Name: "tenant admin"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	keys, err := fx.svc.ListTenant(ctx, fx.tenantID)
	if err != nil || len(keys) != 1 || keys[0].ID != created.Key.ID {
		t.Fatalf("list tenant: %v keys=%+v", err, keys)
	}
	revoked, err := fx.svc.RevokeTenant(ctx, fx.tenantID, created.Key.ID, "admin-actor")
	if err != nil || !revoked.Revoked {
		t.Fatalf("tenant revoke: %v key=%+v", err, revoked)
	}
	if err := fx.svc.DeleteTenant(ctx, fx.tenantID, created.Key.ID, "admin-actor"); err != nil {
		t.Fatalf("tenant delete: %v", err)
	}
	if _, err := fx.svc.GetTenant(ctx, fx.tenantID, created.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted key to disappear from tenant view: %v", err)
	}
}

func TestExpiredDisabledTenantAndScopeStates(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{
		Name:   "stateful",
		Scopes: []string{rbac.PointAlarmAck},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`UPDATE api_key SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		result.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired key: %v", err)
	}
	if _, err := fx.svc.RotateSelf(ctx, fx.userID, result.Key.ID, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expired key rotation must be rejected: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`UPDATE api_key SET expires_at = now() + interval '1 day' WHERE id = $1`,
		result.Key.ID); err != nil {
		t.Fatal(err)
	}

	fx.setRole(t, rbac.RoleReadOnly)
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrScopeNotAllowed) {
		t.Fatalf("demoted owner scope: %v", err)
	}
	fx.setRole(t, rbac.RoleEngineer)

	if _, err := fx.pool.Exec(ctx,
		`UPDATE app_user SET disabled = TRUE WHERE id = $1`, fx.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled user: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`UPDATE app_user SET disabled = FALSE WHERE id = $1`, fx.userID); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.pool.Exec(ctx,
		`UPDATE tenant SET status = 'SUSPENDED' WHERE id = $1`, fx.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrTenantInactive) {
		t.Fatalf("inactive tenant: %v", err)
	}
}

func TestAuthenticateLockout(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{Name: "lockout"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wrong := mustKey(t)
	// Give the wrong credential the same stored prefix so the lock is keyed
	// to the real key identifier, matching the service-account pattern.
	wrong = result.Plaintext[:storedPrefixLength] + wrong[storedPrefixLength:]

	sawLock := false
	for i := 0; i < lockoutThreshold+2; i++ {
		_, err := fx.svc.Authenticate(ctx, wrong, "ip")
		switch {
		case errors.Is(err, ErrLocked):
			sawLock = true
		case errors.Is(err, ErrInvalidCredentials):
		default:
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if !sawLock {
		t.Fatal("expected repeated failures to lock the key prefix")
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrLocked) {
		t.Fatalf("correct key must still be blocked during lockout: %v", err)
	}
}

func TestRotateAndHardDelete(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{Name: "rotate"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := fx.svc.RotateSelf(ctx, fx.userID, result.Key.ID, &past); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("past expiry must be rejected: %v", err)
	}
	rotated, err := fx.svc.RotateSelf(ctx, fx.userID, result.Key.ID, nil)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Plaintext == result.Plaintext || rotated.OldPrefix != result.Key.KeyPrefix {
		t.Fatal("rotation must issue a new credential and retain the old prefix in audit")
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old credential must stop working: %v", err)
	}
	if _, err := fx.svc.Authenticate(ctx, rotated.Plaintext, "ip"); err != nil {
		t.Fatalf("new credential: %v", err)
	}
	if !hasAuditAction(fx.auditor.events, "rotate-api-key") {
		t.Fatalf("expected rotate audit event, got %+v", fx.auditor.events)
	}
	if err := fx.svc.DeleteSelf(ctx, fx.userID, result.Key.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := fx.svc.GetSelf(ctx, fx.userID, result.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hard delete: %v", err)
	}
	if !hasAuditAction(fx.auditor.events, "delete-api-key") {
		t.Fatalf("expected delete audit event, got %+v", fx.auditor.events)
	}
	if _, err := fx.svc.Authenticate(ctx, rotated.Plaintext, "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("deleted key must be indistinguishable from unknown: %v", err)
	}
}

func TestLastUsedThrottle(t *testing.T) {
	fx := testService(t)
	ctx := context.Background()
	fx.svc.lastUsedThrottle = time.Minute
	result, err := fx.svc.Create(ctx, fx.userID, CreateInput{Name: "throttle"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := fx.pool.QueryRow(ctx,
		`SELECT last_used_at::text FROM api_key WHERE id = $1`, result.Key.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Authenticate(ctx, result.Plaintext, "ip"); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := fx.pool.QueryRow(ctx,
		`SELECT last_used_at::text FROM api_key WHERE id = $1`, result.Key.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("last_used_at update was not throttled: %s != %s", first, second)
	}
}

func TestCreateRejectsScopeOutsideOwnerPermissions(t *testing.T) {
	fx := testService(t)
	_, err := fx.svc.Create(context.Background(), fx.userID, CreateInput{
		Name:   "too broad",
		Scopes: []string{rbac.PointServiceAccountWrite},
	})
	if !errors.Is(err, ErrScopeNotAllowed) {
		t.Fatalf("expected scope rejection, got %v", err)
	}
}

func mustKey(t *testing.T) string {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
