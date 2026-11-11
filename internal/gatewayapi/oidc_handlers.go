// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// oidc_handlers.go exposes the external IdP single sign-on endpoints
// (ADR-0021): GET /v1/auth/oidc/login starts the authorization-code + PKCE
// flow by redirecting to the IdP, and GET /v1/auth/oidc/callback completes it
// and redirects back into the SPA with a local session token pair delivered
// in the URL fragment (never logged, never sent to a server other than us).

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	appoidc "github.com/nereidai/nereidlink/internal/oidc"

	"github.com/nereidai/nereidlink/internal/identity"
)

// OIDCService is the OIDC surface the handlers drive. The wiring layer
// adapts *oidc.Service to this narrow interface.
type OIDCService interface {
	// AuthURL returns the IdP authorization endpoint URL.
	AuthURL(redirectTo string) (string, error)
	// Complete exchanges the code and returns a local token pair plus the
	// SPA path to land on after login.
	Complete(ctx context.Context, code, state, ip, userAgent string) (identity.TokenPair, string, error)
}

// SetOIDCService wires external SSO. A nil service leaves SSO disabled and
// the routes answer 404. frontendBase is where the browser is returned after
// the IdP callback.
func (h *Hub) SetOIDCService(service OIDCService, frontendBase string) {
	h.oidcService = service
	h.oidcFrontendBase = frontendBase
}

// handleOIDCStatus reports whether external SSO is configured, so the SPA can
// show the SSO button without guessing.
func (h *Hub) handleOIDCStatus(w http.ResponseWriter, r *http.Request) {
	writeRESTJSON(w, http.StatusOK, struct {
		Enabled bool `json:"enabled"`
	}{Enabled: h.oidcService != nil})
}

// handleOIDCLogin redirects the browser to the IdP authorization endpoint.
func (h *Hub) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if h.oidcService == nil {
		http.NotFound(w, r)
		return
	}
	redirectTo := r.URL.Query().Get("redirect_to")
	loc, err := h.oidcService.AuthURL(redirectTo)
	if err != nil {
		h.redirectOIDCError(w, r, "oidc_start_failed")
		return
	}
	// loc is the IdP authorization endpoint derived from server-side
	// discovery/config, not user input; redirectTo only becomes an opaque
	// state/query value carried through the IdP round-trip.
	http.Redirect(w, r, loc, http.StatusFound) //nolint:gosec // G107 trusted IdP URL
}

// handleOIDCCallback verifies the IdP response and returns into the SPA.
func (h *Hub) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if h.oidcService == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		h.redirectOIDCError(w, r, errParam)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		h.redirectOIDCError(w, r, "oidc_missing_code")
		return
	}
	pair, redirectTo, err := h.oidcService.Complete(r.Context(), code, state,
		requestIP(r), r.UserAgent())
	if err != nil {
		h.redirectOIDCError(w, r, oidcErrorCode(err))
		return
	}

	payload, err := json.Marshal(struct {
		identity.TokenPair
		RedirectTo string `json:"redirect_to"`
	}{TokenPair: pair, RedirectTo: redirectTo})
	if err != nil {
		h.redirectOIDCError(w, r, "oidc_internal")
		return
	}
	base := h.oidcFrontendBase
	if base == "" {
		base = requestRoot(r)
	}
	http.Redirect(w, r,
		base+"/auth/callback#session="+url.QueryEscape(string(payload)),
		http.StatusFound)
}

// redirectOIDCError returns the browser to the SPA login with an error code.
func (h *Hub) redirectOIDCError(w http.ResponseWriter, r *http.Request, code string) {
	base := h.oidcFrontendBase
	if base == "" {
		base = requestRoot(r)
	}
	http.Redirect(w, r,
		base+"/login?oidc_error="+url.QueryEscape(code),
		http.StatusFound)
}

// requestRoot derives the site root from the request Host (same-origin SPA and
// API in the default deployment).
func requestRoot(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	return scheme + "://" + r.Host
}

// oidcErrorCode maps a backend OIDC error to a short fragment/query code the
// SPA localizes, without leaking IdP internals into the URL.
func oidcErrorCode(err error) string {
	switch {
	case errors.Is(err, appoidc.ErrInvalidState):
		return "oidc_invalid_state"
	case errors.Is(err, appoidc.ErrNoEmail):
		return "oidc_no_email"
	case errors.Is(err, appoidc.ErrNoRole):
		return "oidc_no_role"
	case errors.Is(err, identity.ErrUserDisabled):
		return "account_disabled"
	default:
		return "oidc_failed"
	}
}
