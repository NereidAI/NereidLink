// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// api_key_handlers.go exposes self-service and tenant-administrator
// management for user-scoped long-lived API keys. Inbound authentication
// itself is handled by Hub.authorize through the X-Api-Key header.
package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/userapikey"
)

// APIKeyAdmin is the user API-key lifecycle surface.
type APIKeyAdmin interface {
	Create(ctx context.Context, ownerID string, input userapikey.CreateInput) (userapikey.CreateResult, error)
	ListSelf(ctx context.Context, userID string) ([]userapikey.APIKey, error)
	GetSelf(ctx context.Context, userID, keyID string) (userapikey.APIKey, error)
	RevokeSelf(ctx context.Context, userID, keyID string) (userapikey.APIKey, error)
	DeleteSelf(ctx context.Context, userID, keyID string) error
	RotateSelf(ctx context.Context, userID, keyID string, newExpiry *time.Time) (userapikey.RotateResult, error)
	ListTenant(ctx context.Context, tenantID string) ([]userapikey.APIKey, error)
	GetTenant(ctx context.Context, tenantID, keyID string) (userapikey.APIKey, error)
	RevokeTenant(ctx context.Context, tenantID, keyID, actorID string) (userapikey.APIKey, error)
	DeleteTenant(ctx context.Context, tenantID, keyID, actorID string) error
}

// APIKeyAuthenticator verifies an inbound X-Api-Key credential.
type APIKeyAuthenticator interface {
	Authenticate(ctx context.Context, plaintext, peerIP string) (userapikey.AuthResult, error)
}

// SetUserAPIKeys wires API-key management and authentication.
func (h *Hub) SetUserAPIKeys(admin APIKeyAdmin, authenticator APIKeyAuthenticator) {
	h.apiKeyAdmin = admin
	h.apiKeyAuthenticator = authenticator
}

type createAPIKeyBody struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

type rotateAPIKeyBody struct {
	ExpiresAt string `json:"expires_at"`
}

func (h *Hub) requireAPIKeyAdmin(w http.ResponseWriter) APIKeyAdmin {
	if h.apiKeyAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE",
			"user API keys are not enabled")
		return nil
	}
	return h.apiKeyAdmin
}

func requireInteractiveHuman(w http.ResponseWriter, r *http.Request) *rbac.Principal {
	principal := contextPrincipal(r.Context())
	if principal == nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
		return nil
	}
	if principal.Machine || principal.APIKeyID != "" {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"API-key management requires an interactive user session")
		return nil
	}
	return principal
}

func parseOptionalExpiry(w http.ResponseWriter, raw string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	expiresAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "API_KEY_INVALID",
			"expires_at must be an RFC3339 timestamp")
		return nil, false
	}
	return &expiresAt, true
}

func (h *Hub) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	var body createAPIKeyBody
	if !decodeBody(w, r, &body) {
		return
	}
	expiresAt, ok := parseOptionalExpiry(w, body.ExpiresAt)
	if !ok {
		return
	}
	result, err := admin.Create(r.Context(), principal.UserID, userapikey.CreateInput{
		Name: body.Name, Scopes: body.Scopes, ExpiresAt: expiresAt,
	})
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, map[string]any{
		"item":    result.Key,
		"api_key": result.Plaintext,
	})
}

func (h *Hub) handleListOwnAPIKeys(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	keys, err := admin.ListSelf(r.Context(), principal.UserID)
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": orEmptyAPIKeys(keys)})
}

func (h *Hub) handleGetOwnAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	key, err := admin.GetSelf(r.Context(), principal.UserID, r.PathValue("key"))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, key)
}

func (h *Hub) handleRevokeOwnAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	key, err := admin.RevokeSelf(r.Context(), principal.UserID, r.PathValue("key"))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, key)
}

func (h *Hub) handleRotateOwnAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	var body rotateAPIKeyBody
	if r.ContentLength != 0 && !decodeBody(w, r, &body) {
		return
	}
	expiresAt, ok := parseOptionalExpiry(w, body.ExpiresAt)
	if !ok {
		return
	}
	result, err := admin.RotateSelf(r.Context(), principal.UserID,
		r.PathValue("key"), expiresAt)
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"item":       result.Key,
		"api_key":    result.Plaintext,
		"old_prefix": result.OldPrefix,
	})
}

func (h *Hub) handleDeleteOwnAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	if err := admin.DeleteSelf(r.Context(), principal.UserID,
		r.PathValue("key")); err != nil {
		writeAPIKeyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handleListTenantAPIKeys(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	if requireInteractiveHuman(w, r) == nil {
		return
	}
	keys, err := admin.ListTenant(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": orEmptyAPIKeys(keys)})
}

func (h *Hub) handleGetTenantAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	if requireInteractiveHuman(w, r) == nil {
		return
	}
	key, err := admin.GetTenant(r.Context(), r.PathValue("tenant"), r.PathValue("key"))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, key)
}

func (h *Hub) handleRevokeTenantAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	key, err := admin.RevokeTenant(r.Context(), r.PathValue("tenant"),
		r.PathValue("key"), principal.UserID)
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, key)
}

func (h *Hub) handleDeleteTenantAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := h.requireAPIKeyAdmin(w)
	if admin == nil {
		return
	}
	principal := requireInteractiveHuman(w, r)
	if principal == nil {
		return
	}
	if err := admin.DeleteTenant(r.Context(), r.PathValue("tenant"),
		r.PathValue("key"), principal.UserID); err != nil {
		writeAPIKeyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func orEmptyAPIKeys(keys []userapikey.APIKey) []userapikey.APIKey {
	if keys == nil {
		return []userapikey.APIKey{}
	}
	return keys
}

func writeAPIKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, userapikey.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "API_KEY_NOT_FOUND", err.Error())
	case errors.Is(err, userapikey.ErrConflict):
		writeRESTError(w, http.StatusConflict, "API_KEY_CONFLICT", err.Error())
	case errors.Is(err, userapikey.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "API_KEY_INVALID", err.Error())
	case errors.Is(err, userapikey.ErrScopeNotAllowed):
		writeRESTError(w, http.StatusForbidden, "API_KEY_SCOPE_NOT_ALLOWED", err.Error())
	case errors.Is(err, userapikey.ErrUserDisabled):
		writeRESTError(w, http.StatusForbidden, "USER_DISABLED", err.Error())
	case errors.Is(err, userapikey.ErrTenantInactive):
		writeRESTError(w, http.StatusForbidden, "TENANT_INACTIVE", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "API_KEY_OPERATION_FAILED", err.Error())
	}
}
