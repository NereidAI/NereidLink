// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secret_binding_handlers.go exposes edge secret delivery bindings over
// tenant REST (ADR-0034). The gateway id is the gateway device UUID; routes
// are mapped to edge_secret:read / edge_secret:write in authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/secretbinding"
)

// SecretBindingAdmin is the secret delivery administration surface.
type SecretBindingAdmin interface {
	// Bindings lists the gateway's active (non-removed) bindings.
	Bindings(ctx context.Context, tenantID, gatewayID string) ([]secretbinding.Binding, error)
	// Deliver upserts the desired binding for one named secret.
	Deliver(ctx context.Context, tenantID, gatewayID, secretName string,
		selectors []secretbinding.Selector) (secretbinding.Binding, error)
	// Withdraw removes the secret from the gateway on its next sync.
	Withdraw(ctx context.Context, tenantID, gatewayID, secretName string) error
}

// SetSecretBindingAdmin wires the secret delivery service.
func (h *Hub) SetSecretBindingAdmin(admin SecretBindingAdmin) {
	h.secretBindingAdmin = admin
}

// secretBindingBody is the create/update request body.
type secretBindingBody struct {
	// SecretName identifies the vault secret; required on POST to the
	// collection, ignored on PUT/DELETE of an item.
	SecretName string `json:"secret_name"`
	// Selectors carries the generic per-gateway target list.
	Selectors []secretbinding.Selector `json:"selectors"`
}

// handleSecretBindings lists or creates bindings for one gateway.
func (h *Hub) handleSecretBindings(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.secretBindingCallReady(w)
	if !ok {
		return
	}
	tenant, gateway := r.PathValue("tenant"), r.PathValue("gateway")
	switch r.Method {
	case http.MethodGet:
		bindings, err := admin.Bindings(r.Context(), tenant, gateway)
		if err != nil {
			writeSecretBindingError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, bindings)
	case http.MethodPost:
		var body secretBindingBody
		if !decodeBody(w, r, &body) {
			return
		}
		binding, err := admin.Deliver(r.Context(), tenant, gateway,
			body.SecretName, body.Selectors)
		if err != nil {
			writeSecretBindingError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, binding)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSecretBinding operates on one binding by secret name: PUT updates
// the target selectors (idempotent re-deliver), DELETE withdraws.
func (h *Hub) handleSecretBinding(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.secretBindingCallReady(w)
	if !ok {
		return
	}
	tenant, gateway := r.PathValue("tenant"), r.PathValue("gateway")
	secretName := r.PathValue("secret")
	switch r.Method {
	case http.MethodPut:
		var body secretBindingBody
		if !decodeBody(w, r, &body) {
			return
		}
		binding, err := admin.Deliver(r.Context(), tenant, gateway,
			secretName, body.Selectors)
		if err != nil {
			writeSecretBindingError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, binding)
	case http.MethodDelete:
		if err := admin.Withdraw(r.Context(), tenant, gateway, secretName); err != nil {
			writeSecretBindingError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// secretBindingCallReady verifies the delivery service is wired.
func (h *Hub) secretBindingCallReady(w http.ResponseWriter) (SecretBindingAdmin, bool) {
	if h.secretBindingAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_BINDING_SERVICE_UNAVAILABLE",
			"secret delivery management is not enabled")
		return nil, false
	}
	return h.secretBindingAdmin, true
}

// writeSecretBindingError maps delivery errors to HTTP status codes.
func writeSecretBindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secretbinding.ErrSecretNotFound):
		writeRESTError(w, http.StatusNotFound, "SECRET_NOT_FOUND", err.Error())
	case errors.Is(err, secretbinding.ErrSecretNotDeliverable):
		writeRESTError(w, http.StatusBadRequest, "SECRET_NOT_DELIVERABLE", err.Error())
	case errors.Is(err, secretbinding.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "SECRET_BINDING_NOT_FOUND", err.Error())
	case errors.Is(err, secretbinding.ErrInvalidSelector):
		writeRESTError(w, http.StatusBadRequest, "SECRET_SELECTOR_INVALID", err.Error())
	case errors.Is(err, secretbinding.ErrEmptySecret):
		writeRESTError(w, http.StatusBadRequest, "SECRET_NAME_REQUIRED", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "SECRET_BINDING_OPERATION_FAILED", err.Error())
	}
}
