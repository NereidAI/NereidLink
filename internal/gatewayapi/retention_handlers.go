// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// retention_handlers.go exposes per-tenant retention policy and legal
// hold (ADR-0005). Members read effective policy; only a platform
// administrator changes it. Automatic deletion still runs through the
// background enforcer; this surface only edits policy.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/retention"
)

// RetentionStore is the retention policy persistence surface.
type RetentionStore interface {
	List(ctx context.Context, tenantID string) ([]retention.Policy, error)
	Get(ctx context.Context, tenantID, domain string) (retention.Policy, error)
	Set(ctx context.Context, tenantID, domain string, in retention.SetInput) (retention.Policy, error)
}

// SetRetentionStore wires the retention policy store.
func (h *Hub) SetRetentionStore(store RetentionStore) {
	h.retentionStore = store
}

// handleRetentionList returns effective policy for every domain.
func (h *Hub) handleRetentionList(w http.ResponseWriter, r *http.Request) {
	if h.retentionStore == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RETENTION_SERVICE_UNAVAILABLE",
			"retention management is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	policies, err := h.retentionStore.List(r.Context(), tenant)
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": policies})
}

// handleRetentionGet returns one domain policy.
func (h *Hub) handleRetentionGet(w http.ResponseWriter, r *http.Request) {
	if h.retentionStore == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RETENTION_SERVICE_UNAVAILABLE",
			"retention management is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	domain := r.PathValue("domain")
	policy, err := h.retentionStore.Get(r.Context(), tenant, domain)
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, policy)
}

// retentionBody carries one domain policy update.
type retentionBody struct {
	// RetentionDays is the new window in days; 0 keeps data indefinitely.
	RetentionDays int `json:"retention_days"`

	// LegalHold suspends automatic deletion.
	LegalHold bool `json:"legal_hold"`
}

// handleRetentionPut changes one tenant/domain policy. Retention is
// deployment policy, so a platform administrator is required.
func (h *Hub) handleRetentionPut(w http.ResponseWriter, r *http.Request) {
	if h.retentionStore == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RETENTION_SERVICE_UNAVAILABLE",
			"retention management is not enabled")
		return
	}
	principal := contextPrincipal(r.Context())
	if principal == nil || !principal.PlatformAdmin {
		writeRESTError(w, http.StatusForbidden, "RETENTION_FORBIDDEN",
			"only a platform administrator may change retention policy")
		return
	}
	tenant := r.PathValue("tenant")
	domain := r.PathValue("domain")
	var body retentionBody
	if !decodeBody(w, r, &body) {
		return
	}
	policy, err := h.retentionStore.Set(r.Context(), tenant, domain, retention.SetInput{
		RetentionDays: body.RetentionDays,
		LegalHold:     body.LegalHold,
	})
	if err != nil {
		writeRetentionError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, policy)
}

// writeRetentionError maps retention errors to HTTP status codes.
func writeRetentionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, retention.ErrInvalidDomain):
		writeRESTError(w, http.StatusBadRequest, "RETENTION_INVALID_DOMAIN", err.Error())
	case errors.Is(err, retention.ErrInvalidDays):
		writeRESTError(w, http.StatusBadRequest, "RETENTION_INVALID_DAYS", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "RETENTION_OPERATION_FAILED", err.Error())
	}
}
