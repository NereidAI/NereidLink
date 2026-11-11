// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// escalation_handlers.go exposes alarm escalation chain CRUD (ADR-0019).
// Routes live under the tenant prefix and are mapped to the
// escalation:read / escalation:write permission points in authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/escalation"
)

// EscalationAdmin is the escalation chain persistence surface.
type EscalationAdmin interface {
	List(ctx context.Context, tenantID string) ([]escalation.Chain, error)
	Get(ctx context.Context, tenantID, id string) (escalation.Chain, error)
	Create(ctx context.Context, tenantID string, input escalation.SaveInput) (escalation.Chain, error)
	Update(ctx context.Context, tenantID, id string, input escalation.SaveInput) (escalation.Chain, error)
	Delete(ctx context.Context, tenantID, id string) error
}

// SetEscalationAdmin wires the escalation chain service.
func (h *Hub) SetEscalationAdmin(admin EscalationAdmin) {
	h.escalationAdmin = admin
}

// handleEscalationChains lists or creates chains.
func (h *Hub) handleEscalationChains(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.escalationCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		chains, err := admin.List(r.Context(), tenant)
		if err != nil {
			writeEscalationError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, chains)
	case http.MethodPost:
		var input escalation.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		chain, err := admin.Create(r.Context(), tenant, input)
		if err != nil {
			writeEscalationError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, chain)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleEscalationChain operates on one chain by id.
func (h *Hub) handleEscalationChain(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.escalationCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	id := r.PathValue("chain")
	switch r.Method {
	case http.MethodGet:
		chain, err := admin.Get(r.Context(), tenant, id)
		if err != nil {
			writeEscalationError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, chain)
	case http.MethodPut:
		var input escalation.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		chain, err := admin.Update(r.Context(), tenant, id, input)
		if err != nil {
			writeEscalationError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, chain)
	case http.MethodDelete:
		if err := admin.Delete(r.Context(), tenant, id); err != nil {
			writeEscalationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// escalationCallReady verifies the escalation service is wired.
func (h *Hub) escalationCallReady(w http.ResponseWriter) (EscalationAdmin, bool) {
	if h.escalationAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "ESCALATION_SERVICE_UNAVAILABLE",
			"escalation chain management is not enabled")
		return nil, false
	}
	return h.escalationAdmin, true
}

// writeEscalationError maps escalation errors to HTTP status codes.
func writeEscalationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, escalation.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "ESCALATION_CHAIN_NOT_FOUND", err.Error())
	case errors.Is(err, escalation.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "ESCALATION_CHAIN_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "ESCALATION_OPERATION_FAILED", err.Error())
	}
}
