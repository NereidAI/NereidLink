// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_account_handlers_test.go covers the OAuth2 token endpoint
// (JSON, form and Basic credentials; grant validation; error mapping)
// and the service-account management handlers with stub services.

package gatewayapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/machineauth"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// stubMachineAdmin records calls and returns canned results.
type stubMachineAdmin struct {
	createErr  error
	updateErr  error
	getErr     error
	listErr    error
	deleteErr  error
	enabledErr error
	rotateErr  error
}

func (s *stubMachineAdmin) Create(_ context.Context, _ string,
	in machineauth.CreateInput) (machineauth.CreateResult, error) {
	if s.createErr != nil {
		return machineauth.CreateResult{}, s.createErr
	}
	return machineauth.CreateResult{
		Account: machineauth.ServiceAccount{ID: "a1", Name: in.Name, Scopes: in.Scopes},
		Credentials: machineauth.Credentials{
			ClientID: "sa_test", ClientSecret: "shh",
		},
	}, nil
}

func (s *stubMachineAdmin) Update(_ context.Context, _, _ string,
	_ *string, _ []string, _ bool) (machineauth.ServiceAccount, error) {
	return machineauth.ServiceAccount{ID: "a1"}, s.updateErr
}
func (s *stubMachineAdmin) List(_ context.Context, _ string) ([]machineauth.ServiceAccount, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}
func (s *stubMachineAdmin) Get(_ context.Context, _ string, _ string) (machineauth.ServiceAccount, error) {
	return machineauth.ServiceAccount{}, s.getErr
}
func (s *stubMachineAdmin) SetEnabled(_ context.Context, _, _ string, _ bool) (machineauth.ServiceAccount, error) {
	return machineauth.ServiceAccount{}, s.enabledErr
}
func (s *stubMachineAdmin) Delete(_ context.Context, _ string, _ string) error {
	return s.deleteErr
}
func (s *stubMachineAdmin) RotateSecret(_ context.Context, _, _ string,
	_ *time.Time) (machineauth.RotateResult, error) {
	return machineauth.RotateResult{ClientID: "sa_test", NewSecret: "new"}, s.rotateErr
}

// stubGranter returns a configured error or a canned token.
type stubGranter struct {
	err error
}

func (s *stubGranter) IssueToken(_ context.Context, _, _, _ string) (machineauth.TokenResult, error) {
	if s.err != nil {
		return machineauth.TokenResult{}, s.err
	}
	return machineauth.TokenResult{
		AccessToken: "tok", ExpiresAt: time.Now().Add(30 * time.Minute),
	}, nil
}

func tokenHub(admin *stubMachineAdmin, granter *stubGranter) *Hub {
	h := &Hub{}
	h.SetMachineAuth(admin, granter)
	return h
}

// TestTokenJSONSuccess covers a JSON client_credentials grant.
func TestTokenJSONSuccess(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})
	body := `{"grant_type":"client_credentials","client_id":"sa_test","client_secret":"shh"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.handleToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"access_token":"tok"`) ||
		!strings.Contains(rec.Body.String(), `"token_type":"Bearer"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// TestTokenFormAndBasic covers the standards-shaped transports.
func TestTokenFormAndBasic(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})

	// Form body.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/token",
		strings.NewReader("grant_type=client_credentials&client_id=sa_test&client_secret=shh"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.handleToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("form: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Basic auth overrides the (wrong) body credentials.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/token",
		strings.NewReader("grant_type=client_credentials&client_id=ignored&client_secret=ignored"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("sa_test", "shh")
	h.handleToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("basic: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTokenGrantValidation covers the malformed-request cases.
func TestTokenGrantValidation(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"bad grant", `{"grant_type":"password","client_id":"a","client_secret":"b"}`,
			http.StatusBadRequest, "unsupported_grant_type"},
		{"missing secret", `{"grant_type":"client_credentials","client_id":"a"}`,
			http.StatusBadRequest, "invalid_request"},
		{"bad json", `{not json`, http.StatusBadRequest, "INVALID_REQUEST_BODY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/auth/token", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.handleToken(rec, req)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestTokenUnsupportedMediaType covers the content-type guard.
func TestTokenUnsupportedMediaType(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/token", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	h.handleToken(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

// TestTokenErrorMapping covers granter failure to OAuth error mapping.
func TestTokenErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{machineauth.ErrLocked, http.StatusTooManyRequests, "invalid_client"},
		{machineauth.ErrDisabled, http.StatusForbidden, "invalid_client"},
		{machineauth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid_client"},
		{machineauth.ErrNotFound, http.StatusUnauthorized, "invalid_client"},
	}
	for _, tc := range cases {
		h := tokenHub(&stubMachineAdmin{}, &stubGranter{err: tc.err})
		body := `{"grant_type":"client_credentials","client_id":"a","client_secret":"b"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.handleToken(rec, req)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%v: got %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}

// TestTokenUnavailable covers the not-configured response.
func TestTokenUnavailable(t *testing.T) {
	h := &Hub{}
	body := `{"grant_type":"client_credentials"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.handleToken(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// TestCreateServiceAccount covers provisioning and one-time secret.
func TestCreateServiceAccount(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/tenants/t1/service-accounts",
		strings.NewReader(`{"name":"ci","scopes":["device:read"]}`))
	req.SetPathValue("tenant", "t1")
	h.handleCreateServiceAccount(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"client_secret":"shh"`) {
		t.Fatalf("secret must be returned once: %s", rec.Body.String())
	}
}

// TestListServiceAccountsEmptyArray pins the [] contract.
func TestListServiceAccountsEmptyArray(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/service-accounts", nil)
	req.SetPathValue("tenant", "t1")
	h.handleListServiceAccounts(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected empty items array, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestServiceAccountGetNotFound maps the domain error.
func TestServiceAccountGetNotFound(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{getErr: machineauth.ErrNotFound}, &stubGranter{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/t1/service-accounts/a1", nil)
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("account", "a1")
	h.handleGetServiceAccount(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// TestDeleteServiceAccount covers the 204 path.
func TestDeleteServiceAccount(t *testing.T) {
	h := tokenHub(&stubMachineAdmin{}, &stubGranter{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/tenants/t1/service-accounts/a1", nil)
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("account", "a1")
	h.handleDeleteServiceAccount(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

// TestServiceAccountRoutePoints verifies the permission mapping.
func TestServiceAccountRoutePoints(t *testing.T) {
	cases := []struct {
		method string
		rest   []string
		want   string
	}{
		{http.MethodPost, []string{"service-accounts"}, rbac.PointServiceAccountWrite},
		{http.MethodGet, []string{"service-accounts"}, rbac.PointServiceAccountRead},
		{http.MethodGet, []string{"service-accounts", "a"}, rbac.PointServiceAccountRead},
		{http.MethodPost, []string{"service-accounts", "a", "rotate-secret"}, rbac.PointServiceAccountWrite},
		{http.MethodDelete, []string{"service-accounts", "a"}, rbac.PointServiceAccountWrite},
	}
	for _, tc := range cases {
		if got := requiredPoint(tc.method, tc.rest); got != tc.want {
			t.Fatalf("%s %v: got %s want %s", tc.method, tc.rest, got, tc.want)
		}
	}
}

// TestMachinePrincipalBlockedFromAdminRoute verifies the credential
// self-administration guard in enforceRoute.
func TestMachinePrincipalBlockedFromAdminRoute(t *testing.T) {
	machine := rbac.Principal{
		Machine: true,
		Scopes:  []string{rbac.PointAll},
		Tenants: map[string]rbac.TenantGrant{
			"t1": {Scope: rbac.ScopeTenant, Roles: []string{rbac.RoleServiceAccount}},
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/t1/service-accounts", nil)
	if enforceRoute(rec, req, &machine) {
		t.Fatal("a machine principal must never administer service accounts")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// TestMachinePrincipalAllowedOrdinaryRoute verifies the guard does not
// spill beyond service-account routes.
func TestAPIKeyPrincipalBlockedFromServiceAccountAdminRoute(t *testing.T) {
	apiKeyPrincipal := rbac.Principal{
		APIKeyID: "key-1",
		Scopes:   []string{rbac.PointServiceAccountWrite},
		Tenants: map[string]rbac.TenantGrant{
			"t1": {Scope: rbac.ScopeTenant, Roles: []string{rbac.RoleTenantAdmin}},
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/t1/service-accounts", nil)
	if enforceRoute(rec, req, &apiKeyPrincipal) {
		t.Fatal("an API-key principal must never administer service accounts")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestMachinePrincipalAllowedOrdinaryRoute(t *testing.T) {
	machine := rbac.Principal{
		Machine: true,
		Scopes:  []string{rbac.PointDeviceRead},
		Tenants: map[string]rbac.TenantGrant{
			"t1": {Scope: rbac.ScopeTenant, Roles: []string{rbac.RoleServiceAccount}},
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/devices", nil)
	if !enforceRoute(rec, req, &machine) {
		t.Fatalf("a scoped machine principal should pass the read route, got %d", rec.Code)
	}
}
