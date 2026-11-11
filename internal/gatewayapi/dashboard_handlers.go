// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// dashboard_handlers.go exposes tenant dashboard CRUD (ADR-0020). The
// routes live under the tenant prefix and are mapped to the
// dashboard:read / dashboard:write permission points in authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/dashboards"
)

// DashboardAdmin is the dashboard persistence surface.
type DashboardAdmin interface {
	List(ctx context.Context, tenantID string) ([]dashboards.Dashboard, error)
	Get(ctx context.Context, tenantID, id string) (dashboards.Dashboard, error)
	Create(ctx context.Context, tenantID string, input dashboards.Input) (dashboards.Dashboard, error)
	Update(ctx context.Context, tenantID, id string, input dashboards.Input) (dashboards.Dashboard, error)
	Delete(ctx context.Context, tenantID, id string) error
}

// SetDashboardAdmin wires the dashboard service.
func (h *Hub) SetDashboardAdmin(admin DashboardAdmin) {
	h.dashboardAdmin = admin
}

// handleDashboards lists or creates dashboards.
func (h *Hub) handleDashboards(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.dashboardCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		dashboardsList, err := admin.List(r.Context(), tenant)
		if err != nil {
			writeDashboardError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, dashboardsList)
	case http.MethodPost:
		var input dashboards.Input
		if !decodeBody(w, r, &input) {
			return
		}
		dash, err := admin.Create(r.Context(), tenant, input)
		if err != nil {
			writeDashboardError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, dash)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDashboard operates on one dashboard by id.
func (h *Hub) handleDashboard(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.dashboardCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	id := r.PathValue("dashboard")
	switch r.Method {
	case http.MethodGet:
		dash, err := admin.Get(r.Context(), tenant, id)
		if err != nil {
			writeDashboardError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, dash)
	case http.MethodPut:
		var input dashboards.Input
		if !decodeBody(w, r, &input) {
			return
		}
		dash, err := admin.Update(r.Context(), tenant, id, input)
		if err != nil {
			writeDashboardError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, dash)
	case http.MethodDelete:
		if err := admin.Delete(r.Context(), tenant, id); err != nil {
			writeDashboardError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// dashboardCallReady verifies the dashboard service is wired.
func (h *Hub) dashboardCallReady(w http.ResponseWriter) (DashboardAdmin, bool) {
	if h.dashboardAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DASHBOARD_SERVICE_UNAVAILABLE",
			"dashboard management is not enabled")
		return nil, false
	}
	return h.dashboardAdmin, true
}

// writeDashboardError maps dashboard errors to HTTP status codes.
func writeDashboardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dashboards.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "DASHBOARD_NOT_FOUND", err.Error())
	case errors.Is(err, dashboards.ErrConflict):
		writeRESTError(w, http.StatusConflict, "DASHBOARD_CONFLICT", err.Error())
	case errors.Is(err, dashboards.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "DASHBOARD_INVALID_ARGUMENT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "DASHBOARD_OPERATION_FAILED", err.Error())
	}
}
