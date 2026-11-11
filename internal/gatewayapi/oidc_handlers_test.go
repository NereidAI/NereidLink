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

	"github.com/nereidai/nereidlink/internal/identity"
)

type fakeOIDC struct {
	authURL string
	pair    identity.TokenPair
	to      string
	err     error
}

func (f *fakeOIDC) AuthURL(_ string) (string, error) { return f.authURL, nil }
func (f *fakeOIDC) Complete(_ context.Context, _, _, _, _ string) (identity.TokenPair, string, error) {
	return f.pair, f.to, f.err
}

func TestOIDCStatus(t *testing.T) {
	h := &Hub{}
	rec := httptest.NewRecorder()
	h.handleOIDCStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/status", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("disabled status = %s", body)
	}

	h.SetOIDCService(&fakeOIDC{}, "")
	rec = httptest.NewRecorder()
	h.handleOIDCStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/status", nil))
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("enabled status = %s", rec.Body.String())
	}
}

func TestOIDCLoginRedirects(t *testing.T) {
	h := &Hub{}
	fake := &fakeOIDC{authURL: "https://idp.example/authorize?x=1"}
	h.SetOIDCService(fake, "https://app.example")
	rec := httptest.NewRecorder()
	h.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://idp.example/authorize?x=1" {
		t.Fatalf("location = %q", loc)
	}
}

func TestOIDCCallbackSuccessFragment(t *testing.T) {
	h := &Hub{}
	fake := &fakeOIDC{
		pair: identity.TokenPair{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer"},
		to:   "/dash",
	}
	h.SetOIDCService(fake, "https://app.example")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/auth/oidc/callback?code=c&state=s", nil)
	h.handleOIDCCallback(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://app.example/auth/callback#session=") {
		t.Fatalf("location = %q", loc)
	}
	if !strings.Contains(loc, "%22access_token%22%3A%22at%22") {
		t.Fatalf("fragment must carry the token pair: %q", loc)
	}
}

func TestOIDCCallbackErrorRedirects(t *testing.T) {
	h := &Hub{}
	h.SetOIDCService(&fakeOIDC{err: errors.New("boom")}, "https://app.example")
	rec := httptest.NewRecorder()
	h.handleOIDCCallback(rec, httptest.NewRequest(http.MethodGet,
		"/v1/auth/oidc/callback?code=c&state=s", nil))
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://app.example/login?oidc_error=") {
		t.Fatalf("error location = %q", loc)
	}
}

func TestOIDCDisabledIs404(t *testing.T) {
	h := &Hub{}
	rec := httptest.NewRecorder()
	h.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/login", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled login code = %d, want 404", rec.Code)
	}
}
