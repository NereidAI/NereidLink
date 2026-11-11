// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// storage_hook_handlers.go exposes pre-storage hook management and the
// telemetry DLQ (list/delete/replay) over REST.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/storagehook"
)

// HookAdmin is the storage hook management surface.
type HookAdmin interface {
	ListHooks(ctx context.Context, tenantID string) ([]storagehook.HookDTO, error)
	CreateHook(ctx context.Context, tenantID string, input storagehook.HookDTO) (storagehook.HookDTO, error)
	UpdateHook(ctx context.Context, tenantID, hookID string, input storagehook.HookDTO) (storagehook.HookDTO, error)
	DeleteHook(ctx context.Context, tenantID, hookID string) error

	ListDLQ(ctx context.Context, tenantID, hookName, device string, page, pageSize int) (apipage.Page[storagehook.DLQRecord], error)
	DeleteDLQ(ctx context.Context, tenantID string, ids []int64) error
	ReplayDLQ(ctx context.Context, tenantID string, ids []int64) (int, error)
}

// idListBody carries BIGINT identifiers for DLQ operations.
type idListBody struct {
	// IDs are the DLQ row identifiers.
	IDs []int64 `json:"ids"`
}

// SetHookAdmin wires the storage hook management service.
func (h *Hub) SetHookAdmin(admin HookAdmin) {
	h.hookAdmin = admin
}

// handleStorageHooks lists hooks or creates one.
func (h *Hub) handleStorageHooks(w http.ResponseWriter, r *http.Request) {
	if !h.hookAdminReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		hooks, err := h.hookAdmin.ListHooks(r.Context(), tenant)
		if err != nil {
			writeHookError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, hooks)
	case http.MethodPost:
		var input storagehook.HookDTO
		if !decodeBody(w, r, &input) {
			return
		}
		hook, err := h.hookAdmin.CreateHook(r.Context(), tenant, input)
		if err != nil {
			writeHookError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, hook)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleStorageHook updates or deletes one hook.
func (h *Hub) handleStorageHook(w http.ResponseWriter, r *http.Request) {
	if !h.hookAdminReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	hookID := r.PathValue("hook")
	switch r.Method {
	case http.MethodPut:
		var input storagehook.HookDTO
		if !decodeBody(w, r, &input) {
			return
		}
		hook, err := h.hookAdmin.UpdateHook(r.Context(), tenant, hookID, input)
		if err != nil {
			writeHookError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, hook)
	case http.MethodDelete:
		if err := h.hookAdmin.DeleteHook(r.Context(), tenant, hookID); err != nil {
			writeHookError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDLQ lists dead-letter rows of a tenant.
func (h *Hub) handleDLQ(w http.ResponseWriter, r *http.Request) {
	if !h.hookAdminReady(w) {
		return
	}
	page, pageSize := parsePagination(r)
	query := r.URL.Query()
	records, err := h.hookAdmin.ListDLQ(r.Context(),
		r.PathValue("tenant"), query.Get("hook_name"), query.Get("device"), page, pageSize)
	if err != nil {
		writeHookError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, records)
}

// handleDLQDelete removes the supplied DLQ rows.
func (h *Hub) handleDLQDelete(w http.ResponseWriter, r *http.Request) {
	if !h.hookAdminReady(w) {
		return
	}
	var body idListBody
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.hookAdmin.DeleteDLQ(r.Context(), r.PathValue("tenant"), body.IDs); err != nil {
		writeHookError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDLQReplay re-inserts the supplied points into telemetry.
func (h *Hub) handleDLQReplay(w http.ResponseWriter, r *http.Request) {
	if !h.hookAdminReady(w) {
		return
	}
	var body idListBody
	if !decodeBody(w, r, &body) {
		return
	}
	count, err := h.hookAdmin.ReplayDLQ(r.Context(), r.PathValue("tenant"), body.IDs)
	if err != nil {
		writeHookError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]int{"replayed": count})
}

// hookAdminReady verifies the admin service is wired.
func (h *Hub) hookAdminReady(w http.ResponseWriter) bool {
	if h.hookAdmin != nil {
		return true
	}
	writeRESTError(w, http.StatusServiceUnavailable, "STORAGE_HOOK_SERVICE_UNAVAILABLE",
		"storage hook management service is not enabled")
	return false
}

// writeHookError maps storage hook domain errors to HTTP status codes.
func writeHookError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storagehook.ErrHookNotFound):
		writeRESTError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, storagehook.ErrInvalidHook):
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "STORAGE_HOOK_OPERATION_FAILED", err.Error())
	}
}
