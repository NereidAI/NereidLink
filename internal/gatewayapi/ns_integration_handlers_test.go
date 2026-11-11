// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nereidai/nereidlink/internal/sso"
)

type fakeNSIntegration struct {
	secret  string
	tenants []sso.TenantInfo
}

func (f *fakeNSIntegration) ListTenants(context.Context) ([]sso.TenantInfo, error) {
	return f.tenants, nil
}

func (f *fakeNSIntegration) IntegrationSecret() string {
	return f.secret
}

func newNSIntegrationRequest(t *testing.T, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/integration/ns/tenants", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestNSIntegrationTenantsSuccess(t *testing.T) {
	hub := &Hub{nsIntegration: &fakeNSIntegration{
		secret: "ns-link-test-secret",
		tenants: []sso.TenantInfo{
			{TenantID: "6f1e2b7c-9a4d-4c3e-8b21-0d5f7a9c1e42", TenantName: "Acme"},
			{TenantID: "a2c4e6f8-1b3d-4e5f-9a7b-2c4d6e8f0a1b", TenantName: "Globex"},
		},
	}}
	rec := httptest.NewRecorder()
	hub.handleNSIntegrationTenants(rec, newNSIntegrationRequest(t, map[string]string{
		"X-NS-Integration-Secret": "ns-link-test-secret",
		"X-User-Roles":            "user,admin",
		"X-User-Id":               "b1f0c3e2",
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got []sso.TenantInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if len(got) != 2 || got[0].TenantName != "Acme" {
		t.Errorf("unexpected tenants: %+v", got)
	}
}

func TestNSIntegrationTenantsAuth(t *testing.T) {
	hub := &Hub{nsIntegration: &fakeNSIntegration{secret: "topsecret"}}

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"missing secret", map[string]string{"X-User-Roles": "admin"}, http.StatusUnauthorized},
		{"wrong secret", map[string]string{
			"X-NS-Integration-Secret": "forged",
			"X-User-Roles":            "admin",
		}, http.StatusUnauthorized},
		{"spoofed identity without secret", map[string]string{"X-User-Roles": "admin"},
			http.StatusUnauthorized},
		{"valid secret non-admin", map[string]string{
			"X-NS-Integration-Secret": "topsecret",
			"X-User-Roles":            "user",
		}, http.StatusForbidden},
		{"valid secret empty roles", map[string]string{
			"X-NS-Integration-Secret": "topsecret",
		}, http.StatusForbidden},
		{"valid secret admin role with whitespace", map[string]string{
			"X-NS-Integration-Secret": "topsecret",
			"X-User-Roles":            "user, admin ",
		}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			hub.handleNSIntegrationTenants(rec, newNSIntegrationRequest(t, tt.headers))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestNSIntegrationTenantsUnconfigured(t *testing.T) {
	hub := &Hub{}
	rec := httptest.NewRecorder()
	hub.handleNSIntegrationTenants(rec, newNSIntegrationRequest(t, map[string]string{
		"X-NS-Integration-Secret": "x",
	}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
