// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/userapikey"
)

type stubAPIKeyAdmin struct {
	createResult userapikey.CreateResult
	err          error
	list         []userapikey.APIKey
	deleted      bool
}

func (s *stubAPIKeyAdmin) Create(_ context.Context, _ string,
	input userapikey.CreateInput) (userapikey.CreateResult, error) {
	if s.err != nil {
		return userapikey.CreateResult{}, s.err
	}
	if s.createResult.Plaintext != "" {
		return s.createResult, nil
	}
	return userapikey.CreateResult{
		Key:       userapikey.APIKey{ID: "key-1", Name: input.Name, Scopes: input.Scopes},
		Plaintext: "nlk_placeholder",
	}, nil
}
func (s *stubAPIKeyAdmin) ListSelf(context.Context, string) ([]userapikey.APIKey, error) {
	return s.list, s.err
}
func (s *stubAPIKeyAdmin) GetSelf(context.Context, string, string) (userapikey.APIKey, error) {
	return userapikey.APIKey{ID: "key-1"}, s.err
}
func (s *stubAPIKeyAdmin) RevokeSelf(context.Context, string, string) (userapikey.APIKey, error) {
	return userapikey.APIKey{ID: "key-1", Revoked: true}, s.err
}
func (s *stubAPIKeyAdmin) DeleteSelf(context.Context, string, string) error {
	s.deleted = true
	return s.err
}
func (s *stubAPIKeyAdmin) RotateSelf(context.Context, string, string, *time.Time) (userapikey.RotateResult, error) {
	return userapikey.RotateResult{Plaintext: "nlk_rotated", OldPrefix: "nlk_old"}, s.err
}
func (s *stubAPIKeyAdmin) ListTenant(context.Context, string) ([]userapikey.APIKey, error) {
	return s.list, s.err
}
func (s *stubAPIKeyAdmin) GetTenant(context.Context, string, string) (userapikey.APIKey, error) {
	return userapikey.APIKey{ID: "key-1"}, s.err
}
func (s *stubAPIKeyAdmin) RevokeTenant(context.Context, string, string, string) (userapikey.APIKey, error) {
	return userapikey.APIKey{ID: "key-1", Revoked: true}, s.err
}
func (s *stubAPIKeyAdmin) DeleteTenant(context.Context, string, string, string) error {
	s.deleted = true
	return s.err
}

type stubAPIKeyAuthenticator struct {
	result userapikey.AuthResult
	err    error
}

func (s *stubAPIKeyAuthenticator) Authenticate(context.Context, string, string) (userapikey.AuthResult, error) {
	return s.result, s.err
}

func apiKeyHub(admin APIKeyAdmin, auth APIKeyAuthenticator) *Hub {
	h := &Hub{}
	h.SetUserAPIKeys(admin, auth)
	return h
}

func requestWithPrincipal(method, target, body string, principal *rbac.Principal) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if principal != nil {
		req = req.WithContext(context.WithValue(req.Context(),
			principalContext{}, principal))
	}
	return req
}

func TestCreateAPIKeyHandler(t *testing.T) {
	h := apiKeyHub(&stubAPIKeyAdmin{}, nil)
	rec := httptest.NewRecorder()
	req := requestWithPrincipal(http.MethodPost, "/v1/auth/api-keys",
		`{"name":"ci","scopes":["device:read"]}`,
		&rbac.Principal{UserID: "user-1"})
	req.Header.Set("Content-Type", "application/json")
	h.handleCreateAPIKey(rec, req)
	if rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"api_key":"nlk_placeholder"`) {
		t.Fatalf("expected 201 with one-time key, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyManagementRejectsNonInteractivePrincipal(t *testing.T) {
	h := apiKeyHub(&stubAPIKeyAdmin{}, nil)
	for _, principal := range []*rbac.Principal{
		{UserID: "machine", Machine: true},
		{UserID: "key", APIKeyID: "key-1"},
	} {
		rec := httptest.NewRecorder()
		req := requestWithPrincipal(http.MethodGet, "/v1/auth/api-keys", "", principal)
		h.handleListOwnAPIKeys(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", rec.Code)
		}
	}
}

func TestListAPIKeysEmptyArray(t *testing.T) {
	h := apiKeyHub(&stubAPIKeyAdmin{}, nil)
	rec := httptest.NewRecorder()
	req := requestWithPrincipal(http.MethodGet, "/v1/auth/api-keys", "",
		&rbac.Principal{UserID: "user-1"})
	h.handleListOwnAPIKeys(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected empty items array, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRotateAPIKeyAllowsEmptyBody(t *testing.T) {
	h := apiKeyHub(&stubAPIKeyAdmin{}, nil)
	rec := httptest.NewRecorder()
	req := requestWithPrincipal(http.MethodPost,
		"/v1/auth/api-keys/key-1/rotate", "",
		&rbac.Principal{UserID: "user-1"})
	req.SetPathValue("key", "key-1")
	h.handleRotateOwnAPIKey(rec, req)
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"api_key":"nlk_rotated"`) {
		t.Fatalf("expected 200 rotated key, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAPIKeyHandler(t *testing.T) {
	admin := &stubAPIKeyAdmin{}
	h := apiKeyHub(admin, nil)
	rec := httptest.NewRecorder()
	req := requestWithPrincipal(http.MethodDelete, "/v1/auth/api-keys/key-1", "",
		&rbac.Principal{UserID: "user-1"})
	req.SetPathValue("key", "key-1")
	h.handleDeleteOwnAPIKey(rec, req)
	if rec.Code != http.StatusNoContent || !admin.deleted {
		t.Fatalf("expected 204 and deletion, got %d", rec.Code)
	}
}

func TestAPIKeyErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{userapikey.ErrNotFound, http.StatusNotFound, "API_KEY_NOT_FOUND"},
		{userapikey.ErrInvalidArgument, http.StatusBadRequest, "API_KEY_INVALID"},
		{userapikey.ErrScopeNotAllowed, http.StatusForbidden, "API_KEY_SCOPE_NOT_ALLOWED"},
		{errors.New("boom"), http.StatusInternalServerError, "API_KEY_OPERATION_FAILED"},
	}
	for _, tc := range cases {
		h := apiKeyHub(&stubAPIKeyAdmin{err: tc.err}, nil)
		rec := httptest.NewRecorder()
		req := requestWithPrincipal(http.MethodGet, "/v1/auth/api-keys", "",
			&rbac.Principal{UserID: "user-1"})
		h.handleListOwnAPIKeys(rec, req)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%v: got %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIKeyAuthenticationErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{userapikey.ErrInvalidCredentials, http.StatusUnauthorized, "UNAUTHORIZED"},
		{userapikey.ErrLocked, http.StatusTooManyRequests, "API_KEY_LOCKED"},
		{userapikey.ErrUserDisabled, http.StatusForbidden, "USER_DISABLED"},
		{userapikey.ErrTenantInactive, http.StatusForbidden, "TENANT_INACTIVE"},
	}
	for _, tc := range cases {
		h := apiKeyHub(nil, &stubAPIKeyAuthenticator{err: tc.err})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t/devices", nil)
		if _, ok := h.authenticateAPIKey(rec, req, "nlk_test"); ok {
			t.Fatalf("%v: expected failure", tc.err)
		}
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%v: got %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}

func TestEnforceAPIKeyAdminRoutes(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	interactive := memberPrincipal(false, map[string][]string{
		tenant: {rbac.RoleTenantAdmin},
	})
	apiPrincipal := interactive
	apiPrincipal.APIKeyID = "key-1"
	machine := rbac.Principal{Machine: true, Tenants: map[string]rbac.TenantGrant{
		tenant: {Scope: rbac.ScopeTenant, Roles: []string{rbac.RoleServiceAccount}},
	}}
	path := "/v1/tenants/" + tenant + "/api-keys"
	for _, tc := range []struct {
		name      string
		principal *rbac.Principal
		want      bool
	}{
		{"interactive tenant admin", &interactive, true},
		{"API key principal", &apiPrincipal, false},
		{"machine principal", &machine, false},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if got := enforceRoute(rec, req, tc.principal); got != tc.want {
			t.Fatalf("%s: allowed=%v want %v", tc.name, got, tc.want)
		}
	}
}
