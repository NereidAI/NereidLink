// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sso_handlers.go exposes the NereidSphere (NS) one-time handoff ticket
// SSO endpoints: GET /v1/auth/sso/ticket receives the browser redirect with
// the NS ticket and exchanges it server-to-server, and GET /v1/auth/sso/status
// reports whether NS ticket SSO is configured.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/sso"
)

// SSOService is the NS ticket SSO surface the handlers drive.
type SSOService interface {
	// VerifyAndSession exchanges the ticket with NS, JIT-provisions the user,
	// maps a role, and issues a local session token pair.
	VerifyAndSession(ctx context.Context, ticket, ip, userAgent string) (identity.TokenPair, string, error)
	// CallbackPath returns the registered callback path.
	CallbackPath() string
	// LoginURL returns the NS portal product-launch URL.
	LoginURL() string
}

// SetSSOService wires NS ticket SSO. A nil service leaves SSO disabled and
// the routes answer 404. frontendBase is where the browser is returned after
// successful ticket verification.
func (h *Hub) SetSSOService(service SSOService, frontendBase string) {
	h.ssoService = service
	h.ssoFrontendBase = frontendBase
	h.ssoCallbackPath = service.CallbackPath()
}

// handleSSOStatus reports whether NS ticket SSO is configured, so the SPA can
// show the SSO button without guessing.
func (h *Hub) handleSSOStatus(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		Enabled  bool   `json:"enabled"`
		LoginURL string `json:"login_url,omitempty"`
	}{Enabled: h.ssoService != nil}
	if h.ssoService != nil {
		resp.LoginURL = h.ssoService.LoginURL()
	}
	writeRESTJSON(w, http.StatusOK, resp)
}

// handleSSOTicket receives the browser redirect from NS carrying the ticket,
// exchanges it server-to-server, and redirects back into the SPA with a
// local session token pair delivered in the URL fragment.
func (h *Hub) handleSSOTicket(w http.ResponseWriter, r *http.Request) {
	if h.ssoService == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	ticket := q.Get("ticket")
	if ticket == "" {
		h.redirectSSOError(w, r, "sso_missing_ticket")
		return
	}
	pair, redirectTo, err := h.ssoService.VerifyAndSession(r.Context(), ticket,
		requestIP(r), r.UserAgent())
	if err != nil {
		h.redirectSSOError(w, r, ssoErrorCode(err))
		return
	}

	payload, err := json.Marshal(struct {
		identity.TokenPair
		RedirectTo string `json:"redirect_to"`
	}{TokenPair: pair, RedirectTo: redirectTo})
	if err != nil {
		h.redirectSSOError(w, r, "sso_internal")
		return
	}
	base := h.ssoFrontendBase
	if base == "" {
		base = requestRoot(r)
	}
	http.Redirect(w, r,
		base+"/auth/callback#session="+url.QueryEscape(string(payload)),
		http.StatusFound)
}

// redirectSSOError returns the browser to the SPA login with an error code.
func (h *Hub) redirectSSOError(w http.ResponseWriter, r *http.Request, code string) {
	base := h.ssoFrontendBase
	if base == "" {
		base = requestRoot(r)
	}
	http.Redirect(w, r,
		base+"/login?sso_error="+url.QueryEscape(code),
		http.StatusFound)
}

// ssoErrorCode maps a backend SSO error to a short fragment/query code.
func ssoErrorCode(err error) string {
	switch {
	case errors.Is(err, sso.ErrInvalidTicket):
		return "sso_invalid_ticket"
	case errors.Is(err, sso.ErrTicketStoreUnavailable):
		return "sso_store_unavailable"
	case errors.Is(err, identity.ErrUserDisabled):
		return "account_disabled"
	default:
		return "sso_failed"
	}
}
