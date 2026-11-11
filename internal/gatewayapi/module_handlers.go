// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// module_handlers.go exposes the southbound module registry: management
// endpoints (provision, configure, enable/disable) under the tenant prefix
// and adapter-facing enrollment endpoints authenticated by the enrollment
// token. Modules are platform-level resources, but the management routes
// stay under /v1/tenants/{tenant} to match the existing UI navigation and
// permission boundary; the tenant is not used for module ownership.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nereidai/nereidlink/internal/module"
)

// ModuleAdmin is the module registry management surface.
type ModuleAdmin interface {
	List(ctx context.Context) ([]module.Module, error)
	Get(ctx context.Context, id string) (module.Module, error)
	Create(ctx context.Context, input module.CreateInput) (module.Module, error)
	Update(ctx context.Context, id string, input module.UpdateInput) (module.Module, error)
	Delete(ctx context.Context, id string) error
	Register(ctx context.Context, input module.RegisterInput) (module.Module, error)
	Heartbeat(ctx context.Context, id, token string) (module.Module, error)
}

// moduleAdmin is the wired registry, nil until SetModuleAdmin.
// It shares the interface with the store so handlers stay testable.

// SetModuleAdmin wires the southbound module registry.
func (h *Hub) SetModuleAdmin(admin ModuleAdmin) {
	h.moduleAdmin = admin
}

// createModuleBody is the management provisioning request.
type createModuleBody struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	IngressID string          `json:"ingress_id"`
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config"`
}

// updateModuleBody is the partial update request.
type updateModuleBody struct {
	Name    *string          `json:"name"`
	Enabled *bool            `json:"enabled"`
	Config  *json.RawMessage `json:"config"`
}

// registerBody is the adapter registration request.
type registerBody struct {
	EnrollmentToken string `json:"enrollment_token"`
	Type            string `json:"type"`
	IngressID       string `json:"ingress_id"`
	Version         string `json:"version"`
}

// heartbeatBody is the adapter lease renewal request.
type heartbeatBody struct {
	EnrollmentToken string `json:"enrollment_token"`
}

// requireModuleAdmin returns the registry or writes a 503.
func (h *Hub) requireModuleAdmin(w http.ResponseWriter) ModuleAdmin {
	if h.moduleAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "MODULE_SERVICE_UNAVAILABLE",
			"southbound module registry is not enabled")
		return nil
	}
	return h.moduleAdmin
}

// handleListModules returns all provisioned modules.
func (h *Hub) handleListModules(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	modules, err := admin.List(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "MODULE_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, modules)
}

// handleGetModule returns one module.
func (h *Hub) handleGetModule(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	m, err := admin.Get(r.Context(), r.PathValue("module"))
	if h.writeModuleError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, m)
}

// handleCreateModule provisions a module.
func (h *Hub) handleCreateModule(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	var body createModuleBody
	if !decodeBody(w, r, &body) {
		return
	}
	m, err := admin.Create(r.Context(), module.CreateInput{
		Type:      body.Type,
		Name:      body.Name,
		IngressID: body.IngressID,
		Enabled:   body.Enabled,
		Config:    body.Config,
	})
	if h.writeModuleError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusCreated, m)
}

// handleUpdateModule applies a partial update.
func (h *Hub) handleUpdateModule(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	var body updateModuleBody
	if !decodeBody(w, r, &body) {
		return
	}
	m, err := admin.Update(r.Context(), r.PathValue("module"), module.UpdateInput{
		Name:    body.Name,
		Enabled: body.Enabled,
		Config:  body.Config,
	})
	if h.writeModuleError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, m)
}

// handleDeleteModule removes a provisioned module.
func (h *Hub) handleDeleteModule(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.Delete(r.Context(), r.PathValue("module")); h.writeModuleError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleModuleRegister enrolls an adapter process.
func (h *Hub) handleModuleRegister(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	var body registerBody
	if !decodeBody(w, r, &body) {
		return
	}
	m, err := admin.Register(r.Context(), module.RegisterInput{
		EnrollmentToken: body.EnrollmentToken,
		Type:            body.Type,
		IngressID:       body.IngressID,
		Version:         body.Version,
	})
	if h.writeModuleError(w, err) {
		return
	}
	// Never echo the token back to the adapter surface.
	m.EnrollmentToken = ""
	writeRESTJSON(w, http.StatusOK, m)
}

// handleModuleHeartbeat renews an adapter lease.
func (h *Hub) handleModuleHeartbeat(w http.ResponseWriter, r *http.Request) {
	admin := h.requireModuleAdmin(w)
	if admin == nil {
		return
	}
	var body heartbeatBody
	if !decodeBody(w, r, &body) {
		return
	}
	m, err := admin.Heartbeat(r.Context(), r.PathValue("module"),
		body.EnrollmentToken)
	if h.writeModuleError(w, err) {
		return
	}
	m.EnrollmentToken = ""
	writeRESTJSON(w, http.StatusOK, m)
}

// writeModuleError maps module errors onto REST errors. It reports whether
// an error was written.
func (h *Hub) writeModuleError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, module.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "MODULE_NOT_FOUND", err.Error())
	case errors.Is(err, module.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "MODULE_INVALID_ARGUMENT", err.Error())
	case errors.Is(err, module.ErrInvalidEnrollment):
		writeRESTError(w, http.StatusUnauthorized, "MODULE_ENROLLMENT_REJECTED", err.Error())
	case errors.Is(err, module.ErrTypeMismatch):
		writeRESTError(w, http.StatusBadRequest, "MODULE_TYPE_MISMATCH", err.Error())
	default:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeRESTError(w, http.StatusConflict, "MODULE_CONFLICT",
				"module name or ingress id already exists")
			return true
		}
		writeRESTError(w, http.StatusInternalServerError, "MODULE_OPERATION_FAILED", err.Error())
	}
	return true
}
