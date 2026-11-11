// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// provision_handlers.go exposes the platform-level tenant lifecycle to
// operators: list tenants with provisioning status, create a tenant
// (outbox transaction), read one task and retry a failed task. These
// routes are deliberately NOT under a tenant prefix: they act across
// tenants and belong to the platform permission boundary added with
// ADR-0021/0022.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Sentinel errors returned by ProvisionAdmin implementations so
// handlers can map failures to status codes without importing the
// concrete provisioning package.
var (
	// ErrProvisionNotFound means the task does not exist.
	ErrProvisionNotFound = errors.New("provisioning task not found")

	// ErrProvisionInvalidArgument means the request was malformed.
	ErrProvisionInvalidArgument = errors.New("invalid provisioning argument")
)

// ProvisionTaskDTO is one tenant provisioning task as served to clients.
type ProvisionTaskDTO struct {
	// TenantID is the provisioned tenant identifier.
	TenantID string `json:"tenant_id"`

	// TenantName is the tenant display name.
	TenantName string `json:"tenant_name"`

	// TenantStatus is the live tenant-table state (ACTIVE/SUSPENDED).
	TenantStatus string `json:"tenant_status"`

	// Status is PROVISIONING, ACTIVE or FAILED.
	Status string `json:"status"`

	// Attempts is the number of reconcile attempts made so far.
	Attempts int `json:"attempts"`

	// LastError describes the most recent failure.
	LastError string `json:"last_error"`

	// RunAfter is the earliest retry time / active worker lease.
	RunAfter time.Time `json:"run_after"`
}

// ProvisionAdmin is the tenant provisioning surface.
type ProvisionAdmin interface {
	// CreateTenant enqueues a new tenant and returns its ID.
	CreateTenant(ctx context.Context, name string) (string, error)

	// GetTask returns one provisioning task.
	GetTask(ctx context.Context, tenantID string) (ProvisionTaskDTO, error)

	// ListTasks returns every provisioning task.
	ListTasks(ctx context.Context) ([]ProvisionTaskDTO, error)

	// Retry reschedules a FAILED task immediately.
	Retry(ctx context.Context, tenantID string) error
}

// SetProvisionAdmin wires the tenant provisioning service.
func (h *Hub) SetProvisionAdmin(admin ProvisionAdmin) {
	h.provisionAdmin = admin
}

// createTenantBody is the tenant creation request.
type createTenantBody struct {
	Name string `json:"name"`
}

// requireProvisionAdmin returns the service or writes a 503.
func (h *Hub) requireProvisionAdmin(w http.ResponseWriter) ProvisionAdmin {
	if h.provisionAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROVISION_SERVICE_UNAVAILABLE",
			"tenant provisioning is not enabled")
		return nil
	}
	return h.provisionAdmin
}

// handleListTenants returns all tenants with provisioning status.
func (h *Hub) handleListTenants(w http.ResponseWriter, r *http.Request) {
	admin := h.requireProvisionAdmin(w)
	if admin == nil {
		return
	}
	tasks, err := admin.ListTasks(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TENANT_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, tasks)
}

// handleCreateTenant enqueues a new tenant.
func (h *Hub) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	admin := h.requireProvisionAdmin(w)
	if admin == nil {
		return
	}
	var body createTenantBody
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.licenseAdmin.EnsurePlatformCanCreateTenant(r.Context()); h.writeLicenseError(w, err) {
		return
	}
	tenantID, err := admin.CreateTenant(r.Context(), body.Name)
	if h.writeProvisionError(w, err) {
		return
	}
	task, err := admin.GetTask(r.Context(), tenantID)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TENANT_READ_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusCreated, task)
}

// handleGetTenant returns one tenant provisioning task.
func (h *Hub) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	admin := h.requireProvisionAdmin(w)
	if admin == nil {
		return
	}
	task, err := admin.GetTask(r.Context(), r.PathValue("tenant"))
	if h.writeProvisionError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// handleRetryTenant reschedules a FAILED task.
func (h *Hub) handleRetryTenant(w http.ResponseWriter, r *http.Request) {
	admin := h.requireProvisionAdmin(w)
	if admin == nil {
		return
	}
	if h.writeProvisionError(w, admin.Retry(r.Context(), r.PathValue("tenant"))) {
		return
	}
	task, err := admin.GetTask(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TENANT_READ_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// writeProvisionError maps provisioning errors onto REST errors and
// reports whether an error was written.
func (h *Hub) writeProvisionError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrProvisionNotFound):
		writeRESTError(w, http.StatusNotFound, "TENANT_NOT_FOUND", err.Error())
	case errors.Is(err, ErrProvisionInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "TENANT_INVALID_ARGUMENT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "TENANT_OPERATION_FAILED", err.Error())
	}
	return true
}
