// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// quota_handlers.go exposes per-tenant soft quotas (ADR-0014). Members
// read the effective quota; only a platform administrator changes it.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/quota"
)

// QuotaAdmin is the quota persistence surface.
type QuotaAdmin interface {
	Get(ctx context.Context, tenantID string) (quota.Quota, error)
	Upsert(ctx context.Context, tenantID string, in quota.Write) (quota.Quota, error)
}

// SetQuotaAdmin wires the quota service.
func (h *Hub) SetQuotaAdmin(admin QuotaAdmin) {
	h.quotaAdmin = admin
}

// handleQuota reads or replaces the quota of the path tenant.
func (h *Hub) handleQuota(w http.ResponseWriter, r *http.Request) {
	if h.quotaAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "QUOTA_SERVICE_UNAVAILABLE",
			"tenant quota management is not enabled")
		return
	}
	tenant := r.PathValue("tenant")

	switch r.Method {
	case http.MethodGet:
		result, err := h.quotaAdmin.Get(r.Context(), tenant)
		if err != nil {
			writeQuotaError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, result)
	case http.MethodPut:
		// Quotas are deployment policy; a platform administrator is
		// required even when the path tenant is accessible.
		principal := contextPrincipal(r.Context())
		if principal == nil || !principal.PlatformAdmin {
			writeRESTError(w, http.StatusForbidden, "QUOTA_FORBIDDEN",
				"only a platform administrator may change quotas")
			return
		}
		var input quota.Write
		if !decodeBody(w, r, &input) {
			return
		}
		result, err := h.quotaAdmin.Upsert(r.Context(), tenant, input)
		if err != nil {
			writeQuotaError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, result)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// writeQuotaError maps quota errors to HTTP status codes.
func writeQuotaError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, quota.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "QUOTA_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "QUOTA_OPERATION_FAILED", err.Error())
	}
}
