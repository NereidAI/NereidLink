// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sso_db_test.go proves the ticket-group -> tenant membership flow and
// reconciliation against a real PostgreSQL. Skipped without
// NL_TEST_DATABASE_URL, following the rbac scope_db_test.go convention.

package sso

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// fakeProvisioner is an IdentityProvisioner that resolves to a user row
// the fixture created.
type fakeProvisioner struct {
	userID string
	pool   *pgxpool.Pool
	email  string
}

func (f *fakeProvisioner) UpsertExternalUser(_ context.Context, in identity.ExternalUser) (identity.User, error) {
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO app_user (id, email, display_name, email_verified, tenant_id)
		 VALUES ($1, $2, $3, TRUE, NULLIF($4,'')::uuid)
		 ON CONFLICT DO NOTHING`,
		f.userID, in.Email, in.DisplayName, in.TenantID)
	if err != nil {
		return identity.User{}, err
	}
	return identity.User{ID: f.userID, Email: in.Email}, nil
}

func (f *fakeProvisioner) IssuePairForUser(context.Context, string, string, string) (identity.TokenPair, error) {
	return identity.TokenPair{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 1800, TokenType: "Bearer"}, nil
}

type ssoFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *Service
	ns      *httptest.Server
	userID  string
	tenants []string
	resp    ticketBody
}

func newSSOFixture(t *testing.T, cfg Config) *ssoFixture {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	fx := &ssoFixture{t: t, pool: pool, userID: uuid.NewString()}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user (id, email, display_name, email_verified)
		 VALUES ($1, $2, 'SSO Test', TRUE)`,
		fx.userID, "sso-"+fx.userID[:8]+"@example.com"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	fx.tenants = []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for _, id := range fx.tenants {
		if _, err := pool.Exec(ctx,
			`INSERT INTO tenant (id, name, status) VALUES ($1, $2, 'ACTIVE')`,
			id, "SSO Test "+id[:8]); err != nil {
			t.Fatalf("create tenant: %v", err)
		}
	}
	t.Cleanup(func() {
		// Delete the user first: its home-tenant FK and cascading
		// memberships would otherwise block tenant deletion.
		_, _ = pool.Exec(context.Background(), `DELETE FROM app_user WHERE id = $1`, fx.userID)
		for _, id := range fx.tenants {
			// The license metering worker upserts a daily usage row for
			// every tenant (even on a shared live dev database); that
			// NO ACTION FK blocks tenant deletion unless removed in the
			// same statement (single implicit transaction).
			_, _ = pool.Exec(context.Background(),
				`WITH d AS (
				    DELETE FROM tenant_usage_daily WHERE tenant_id = $1 RETURNING 1
				)
				DELETE FROM tenant WHERE id = $1`, id)
		}
	})

	// NS stand-in: serves the ticket response queued in ticketResp.
	fx.ns = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-NS-Client-Id") != cfg.ClientID ||
			r.Header.Get("X-NS-Client-Secret") != cfg.SSOSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(fx.resp)
	}))
	t.Cleanup(fx.ns.Close)

	cfg.NSBaseURL = fx.ns.URL
	if cfg.GroupRole == nil {
		cfg.GroupRole = map[string]string{
			"admins":    rbac.RoleTenantAdmin,
			"engineers": rbac.RoleEngineer,
			"read_only": rbac.RoleReadOnly,
		}
	}
	svc, err := NewService(cfg, &fakeProvisioner{
		userID: fx.userID, pool: pool, email: "sso-test@example.com",
	}, pool)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	fx.svc = svc
	return fx
}

// resp is the ticket verify response the NS stand-in returns.
type ticketBody struct {
	Identity Identity `json:"identity"`
	To       string   `json:"to"`
}

func (fx *ssoFixture) login(groups []string) {
	fx.t.Helper()
	email := "sso-" + fx.userID[:8] + "@example.com"
	fx.resp = ticketBody{
		Identity: Identity{
			Sub:    uuid.NewString(),
			Email:  email,
			Name:   "SSO Test",
			Groups: groups,
			Roles:  []string{"user"},
		},
		To: "/overview",
	}
	if _, _, err := fx.svc.VerifyAndSession(context.Background(), "ticket-"+uuid.NewString(), "127.0.0.1", "test"); err != nil {
		fx.t.Fatalf("VerifyAndSession: %v", err)
	}
}

// homeTenant reads the account anchor (app_user.tenant_id), "" for NULL.
func (fx *ssoFixture) homeTenant() string {
	fx.t.Helper()
	var home *string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM app_user WHERE id = $1`, fx.userID).Scan(&home); err != nil {
		fx.t.Fatalf("read home tenant: %v", err)
	}
	if home == nil {
		return ""
	}
	return *home
}

func (fx *ssoFixture) memberships() map[string][]string {
	fx.t.Helper()
	rows, err := fx.pool.Query(context.Background(), `
		SELECT m.tenant_id::text, r.role
		  FROM tenant_membership m
		  JOIN membership_role r ON r.membership_id = m.id
		 WHERE m.user_id = $1
		 ORDER BY m.tenant_id::text, r.role`, fx.userID)
	if err != nil {
		fx.t.Fatalf("query memberships: %v", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var tenant, role string
		if err := rows.Scan(&tenant, &role); err != nil {
			fx.t.Fatal(err)
		}
		out[tenant] = append(out[tenant], role)
	}
	return out
}

func TestSSOGroupAssignmentsMaterialise(t *testing.T) {
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	t1, t2 := fx.tenants[0], fx.tenants[1]
	fx.login([]string{
		"/nereidlink/" + t1 + "/admins",
		"/nereidlink/" + t2 + "/engineers",
		"/nereidforge/" + t1 + "/admin", // other product, ignored
	})

	got := fx.memberships()
	if len(got) != 2 {
		t.Fatalf("want 2 memberships, got %d: %v", len(got), got)
	}
	if got[t1][0] != rbac.RoleTenantAdmin {
		t.Errorf("tenant1 role = %v, want [TENANT_ADMIN]", got[t1])
	}
	if got[t2][0] != rbac.RoleEngineer {
		t.Errorf("tenant2 role = %v, want [ENGINEER]", got[t2])
	}
}

func TestSSOReconcileRemovesRevokedAssignment(t *testing.T) {
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	t1, t2 := fx.tenants[0], fx.tenants[1]
	fx.login([]string{
		"/nereidlink/" + t1 + "/admins",
		"/nereidlink/" + t2 + "/read_only",
	})
	// Second login: the t2 assignment was revoked in NS.
	fx.login([]string{"/nereidlink/" + t1 + "/admins"})

	got := fx.memberships()
	if _, ok := got[t2]; ok {
		t.Errorf("revoked tenant membership still present: %v", got[t2])
	}
	if got[t1][0] != rbac.RoleTenantAdmin {
		t.Errorf("retained tenant role = %v, want [TENANT_ADMIN]", got[t1])
	}
}

func TestSSOReconcileRoleChangeSameTenant(t *testing.T) {
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	t1 := fx.tenants[0]
	// First the suite grants read-only, then changes the assignment to admin.
	fx.login([]string{"/nereidlink/" + t1 + "/read_only"})
	fx.login([]string{"/nereidlink/" + t1 + "/admins"})

	got := fx.memberships()
	roles := got[t1]
	if len(roles) != 1 || roles[0] != rbac.RoleTenantAdmin {
		t.Fatalf("role change should converge to exactly [TENANT_ADMIN], got %v", roles)
	}
}

func TestSSOGroupAssignmentAnchorsHomeTenant(t *testing.T) {
	// A freshly JIT-provisioned user with a group assignment must be
	// anchored to the assignment tenant, not the configured fallback.
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	fx.svc.cfg.TenantID = fx.tenants[2]
	t1 := fx.tenants[0]
	fx.login([]string{"/nereidlink/" + t1 + "/admins"})

	if home := fx.homeTenant(); home != t1 {
		t.Fatalf("home tenant = %q, want assigned tenant %q", home, t1)
	}
}

func TestSSOAnchorHealsMisAnchoredAccount(t *testing.T) {
	// Existing account anchored to the fallback tenant (created before its
	// group assignment) is realigned on the next group login.
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	fx.svc.cfg.TenantID = fx.tenants[2]
	t1 := fx.tenants[0]
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE app_user SET tenant_id = $2 WHERE id = $1`,
		fx.userID, fx.tenants[2]); err != nil {
		t.Fatalf("mis-anchor user: %v", err)
	}
	fx.login([]string{"/nereidlink/" + t1 + "/engineers"})

	if home := fx.homeTenant(); home != t1 {
		t.Fatalf("home tenant = %q, want healed tenant %q", home, t1)
	}
}

func TestSSOReconcilePreservesCustomerUser(t *testing.T) {
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		ReconcileMemberships: true,
	})
	t1, tCurated := fx.tenants[0], fx.tenants[2]
	// Locally curated CUSTOMER_USER access on a tenant NS no longer manages.
	if err := rbac.EnsureMembership(context.Background(), fx.pool, fx.userID,
		tCurated, rbac.RoleCustomerUser); err != nil {
		t.Fatalf("seed curated membership: %v", err)
	}
	fx.login([]string{"/nereidlink/" + t1 + "/admins"})

	got := fx.memberships()
	if roles, ok := got[tCurated]; !ok || !contains(roles, rbac.RoleCustomerUser) {
		t.Errorf("curated CUSTOMER_USER membership disturbed: %v", got)
	}
}

func TestSSOGroupAbsentFallback(t *testing.T) {
	fx := newSSOFixture(t, Config{
		ClientID:             "link",
		SSOSecret:            "secret",
		RoleMap:              map[string]string{"user": rbac.RoleReadOnly},
		DefaultRole:          rbac.RoleReadOnly,
		ReconcileMemberships: true,
	})
	// Attach the fallback membership to the first fixture tenant.
	fx.svc.cfg.TenantID = fx.tenants[0]
	// Older NS: the identity carries no groups field at all.
	fx.login(nil)

	got := fx.memberships()
	roles, ok := got[fx.tenants[0]]
	if !ok {
		t.Fatalf("fallback membership missing: %v", got)
	}
	if len(roles) != 1 || roles[0] != rbac.RoleReadOnly {
		t.Errorf("fallback role = %v, want [READ_ONLY]", roles)
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
