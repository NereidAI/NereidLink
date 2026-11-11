// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_account_handlers.go exposes ADR-0021 machine identity
// administration (tenant-local OAuth2 service accounts) and the public
// OAuth2 client_credentials token endpoint.
//
// Management routes are tenant scoped and require the
// serviceaccount:write/read permission points, which only the tenant
// administrator wildcard holds: an automation identity must never be able
// to mint another automation identity.
//
// The token endpoint is deliberately unauthenticated at the gateway (it
// is listed in isExemptPath) because presenting the client secret IS the
// authentication; the service applies its own failure lockout.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/machineauth"
)

// MachineAuthAdmin is the service-account lifecycle surface.
// *machineauth.Service satisfies it.
type MachineAuthAdmin interface {
	Create(ctx context.Context, tenantID string,
		input machineauth.CreateInput) (machineauth.CreateResult, error)
	Update(ctx context.Context, tenantID, accountID string,
		name *string, scopes []string, setScopes bool) (machineauth.ServiceAccount, error)
	List(ctx context.Context, tenantID string) ([]machineauth.ServiceAccount, error)
	Get(ctx context.Context, tenantID, accountID string) (machineauth.ServiceAccount, error)
	SetEnabled(ctx context.Context, tenantID, accountID string,
		enabled bool) (machineauth.ServiceAccount, error)
	Delete(ctx context.Context, tenantID, accountID string) error
	RotateSecret(ctx context.Context, tenantID, accountID string,
		newExpiry *time.Time) (machineauth.RotateResult, error)
}

// MachineTokenGranter authenticates client credentials and issues a
// machine access token.
type MachineTokenGranter interface {
	IssueToken(ctx context.Context, clientID, clientSecret, peerIP string) (machineauth.TokenResult, error)
}

// SetMachineAuth wires service-account administration and the token
// grant. Both surfaces share one machineauth.Service in main.
func (h *Hub) SetMachineAuth(admin MachineAuthAdmin, granter MachineTokenGranter) {
	h.machineAdmin = admin
	h.machineGranter = granter
}

// createServiceAccountBody carries the provisioning request.
type createServiceAccountBody struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// updateServiceAccountBody carries optional field changes. A field is
// changed only when present in the JSON.
type updateServiceAccountBody struct {
	Name   *string   `json:"name"`
	Scopes *[]string `json:"scopes"`
}

// requireMachineAdmin returns the admin surface or writes 503.
func (h *Hub) requireMachineAdmin(w http.ResponseWriter) MachineAuthAdmin {
	if h.machineAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SERVICE_ACCOUNT_UNAVAILABLE",
			"machine identity is not enabled")
		return nil
	}
	return h.machineAdmin
}

// handleCreateServiceAccount provisions an account and returns its
// plaintext secret exactly once.
func (h *Hub) handleCreateServiceAccount(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	var body createServiceAccountBody
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := admin.Create(r.Context(), r.PathValue("tenant"), machineauth.CreateInput{
		Name:   body.Name,
		Scopes: body.Scopes,
	})
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, map[string]any{
		"account":       result.Account,
		"client_id":     result.Credentials.ClientID,
		"client_secret": result.Credentials.ClientSecret,
	})
}

// handleListServiceAccounts returns every service account of a tenant.
func (h *Hub) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	accounts, err := admin.List(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	if accounts == nil {
		accounts = []machineauth.ServiceAccount{}
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": accounts})
}

// handleGetServiceAccount returns one account.
func (h *Hub) handleGetServiceAccount(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	account, err := admin.Get(r.Context(),
		r.PathValue("tenant"), r.PathValue("account"))
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, account)
}

// handleUpdateServiceAccount changes name and/or scopes.
func (h *Hub) handleUpdateServiceAccount(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	var body updateServiceAccountBody
	if !decodeBody(w, r, &body) {
		return
	}
	account, err := admin.Update(r.Context(),
		r.PathValue("tenant"), r.PathValue("account"),
		body.Name, valueOrNil(body.Scopes), body.Scopes != nil)
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, account)
}

// valueOrNil returns the dereferenced optional slice.
func valueOrNil(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// serviceAccountEnabledBody carries the enable/disable switch.
type serviceAccountEnabledBody struct {
	Enabled bool `json:"enabled"`
}

// handleSetServiceAccountEnabled switches authentication for an account.
func (h *Hub) handleSetServiceAccountEnabled(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	var body serviceAccountEnabledBody
	if !decodeBody(w, r, &body) {
		return
	}
	account, err := admin.SetEnabled(r.Context(),
		r.PathValue("tenant"), r.PathValue("account"), body.Enabled)
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, account)
}

// handleRotateServiceAccountSecret performs overlap rotation and
// returns the new plaintext secret exactly once.
func (h *Hub) handleRotateServiceAccountSecret(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	result, err := admin.RotateSecret(r.Context(),
		r.PathValue("tenant"), r.PathValue("account"), nil)
	if err != nil {
		writeMachineAuthError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"account":             result.Account,
		"client_id":           result.ClientID,
		"client_secret":       result.NewSecret,
		"rotating_expires_at": result.RotatingExpiresAt,
	})
}

// handleDeleteServiceAccount removes an account and its secrets.
func (h *Hub) handleDeleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	admin := h.requireMachineAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.Delete(r.Context(),
		r.PathValue("tenant"), r.PathValue("account")); err != nil {
		writeMachineAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokenBody is the JSON encoding of the token request. Standard OAuth2
// normally uses application/x-www-form-urlencoded; both are accepted so a
// modern JSON client and a standards-compliant form client work without
// configuration.
type tokenBody struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Scope        string `json:"scope"`
}

// handleToken implements POST /v1/auth/token (client_credentials only).
// Credentials may come from the JSON/form body or HTTP Basic auth; Basic
// is the RFC 6749 default and the body is the common integration form.
func (h *Hub) handleToken(w http.ResponseWriter, r *http.Request) {
	if h.machineGranter == nil {
		writeOAuthError(w, "invalid_request",
			"machine identity is not enabled", http.StatusServiceUnavailable)
		return
	}

	grantType, clientID, clientSecret, ok := parseTokenRequest(w, r)
	if !ok {
		return
	}
	// Only the machine-identity grant is implemented. Password and
	// authorization-code grants use /v1/auth/login and the OIDC flow.
	if grantType != "client_credentials" {
		writeOAuthError(w, "unsupported_grant_type",
			"only client_credentials is supported", http.StatusBadRequest)
		return
	}
	if clientID == "" || clientSecret == "" {
		writeOAuthError(w, "invalid_request",
			"client_id and client_secret are required", http.StatusBadRequest)
		return
	}

	result, err := h.machineGranter.IssueToken(r.Context(),
		clientID, clientSecret, requestIP(r))
	if err != nil {
		switch {
		case errors.Is(err, machineauth.ErrLocked):
			writeOAuthError(w, "invalid_client", err.Error(), http.StatusTooManyRequests)
		case errors.Is(err, machineauth.ErrDisabled):
			writeOAuthError(w, "invalid_client", err.Error(), http.StatusForbidden)
		default:
			// ErrNotFound and bad secret collapse to invalid_client so
			// the endpoint cannot be used to enumerate client ids.
			writeOAuthError(w, "invalid_client",
				"invalid client credentials", http.StatusUnauthorized)
		}
		return
	}

	writeRESTJSON(w, http.StatusOK, map[string]any{
		"access_token": result.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(result.ExpiresAt).Seconds()),
	})
}

// parseTokenRequest extracts grant type and credentials from form, JSON
// and Basic auth. It reports false after writing the error response.
func parseTokenRequest(w http.ResponseWriter, r *http.Request) (grantType, clientID, clientSecret string, ok bool) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		var body tokenBody
		if !decodeBody(w, r, &body) {
			return "", "", "", false
		}
		grantType, clientID, clientSecret =
			body.GrantType, body.ClientID, body.ClientSecret
	case contentType == "" || strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, "invalid_request", "malformed form body", http.StatusBadRequest)
			return "", "", "", false
		}
		grantType = r.PostForm.Get("grant_type")
		clientID = r.PostForm.Get("client_id")
		clientSecret = r.PostForm.Get("client_secret")
	default:
		writeOAuthError(w, "invalid_request",
			"content type must be JSON or form-urlencoded", http.StatusUnsupportedMediaType)
		return "", "", "", false
	}

	// HTTP Basic overrides body credentials per RFC 6749 section 2.3.1.
	if user, pass, hasBasic := r.BasicAuth(); hasBasic {
		clientID, clientSecret = user, pass
	}
	return grantType, clientID, clientSecret, true
}

// writeOAuthError writes a standards-shaped OAuth2 error response:
// {"error": code, "error_description": message}.
func writeOAuthError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeRESTJSON(w, status, map[string]any{
		"error":             code,
		"error_description": message,
	})
}

// writeMachineAuthError maps service errors to REST status codes.
func writeMachineAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, machineauth.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "SERVICE_ACCOUNT_NOT_FOUND", err.Error())
	case errors.Is(err, machineauth.ErrConflict):
		writeRESTError(w, http.StatusConflict, "SERVICE_ACCOUNT_CONFLICT", err.Error())
	case errors.Is(err, machineauth.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "SERVICE_ACCOUNT_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError,
			"SERVICE_ACCOUNT_OPERATION_FAILED", err.Error())
	}
}
