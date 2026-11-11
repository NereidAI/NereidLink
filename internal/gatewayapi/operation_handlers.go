// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// operation_handlers.go exposes the gateway operation catalog over tenant
// REST (ADR-0035): effective catalog, private descriptor CRUD, clone of an
// edge descriptor, and per-tenant disable/enable. Reads need
// edge_operation:read; all writes edge_operation:exec (the only write point
// the grilling fixed for this surface).

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/opcatalog"
)

// OperationCatalog is the descriptor management surface.
type OperationCatalog interface {
	Effective(ctx context.Context, tenantID string) ([]opcatalog.View, error)
	ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]opcatalog.View, error)
	EffectiveForGateway(ctx context.Context, tenantID, gatewayID, operation string) (opcatalog.View, bool, error)
	CreateManual(ctx context.Context, tenantID string, d opcatalog.Descriptor) error
	UpdateManual(ctx context.Context, tenantID, operation, version string, d opcatalog.Descriptor) error
	DeleteManual(ctx context.Context, tenantID, operation, version string) error
	CloneToPrivate(ctx context.Context, tenantID, operation, version, newKey string) error
	SetDisabled(ctx context.Context, tenantID, operation, version, userID string) error
	ClearDisabled(ctx context.Context, tenantID, operation, version string) error
}

// SetOperationCatalog wires the operation catalog.
func (h *Hub) SetOperationCatalog(catalog OperationCatalog) {
	h.operations = catalog
}

// operationBody carries one private descriptor write.
type operationBody struct {
	Operation    string            `json:"operation"`
	TypeVersion  string            `json:"type_version,omitempty"`
	Interaction  string            `json:"interaction,omitempty"`
	TimeoutMs    int               `json:"timeout_ms,omitempty"`
	Risk         string            `json:"risk,omitempty"`
	ParamSchema  map[string]any    `json:"param_schema,omitempty"`
	ResultSchema map[string]any    `json:"result_schema,omitempty"`
	DisplayName  map[string]string `json:"display_name,omitempty"`
	Description  map[string]string `json:"description,omitempty"`
}

// toDescriptor converts a body to a catalog descriptor.
func (b operationBody) toDescriptor() (opcatalog.Descriptor, error) {
	d := opcatalog.Descriptor{
		Operation:   b.Operation,
		TypeVersion: b.TypeVersion,
		Interaction: b.Interaction,
		TimeoutMs:   b.TimeoutMs,
		Risk:        b.Risk,
		DisplayName: b.DisplayName,
		Description: b.Description,
	}
	if len(b.ParamSchema) > 0 {
		raw, err := json.Marshal(b.ParamSchema)
		if err != nil {
			return opcatalog.Descriptor{}, err
		}
		d.ParamSchema = raw
	}
	if len(b.ResultSchema) > 0 {
		raw, err := json.Marshal(b.ResultSchema)
		if err != nil {
			return opcatalog.Descriptor{}, err
		}
		d.ResultSchema = raw
	}
	return d, nil
}

// disabledBody toggles one key.
type disabledBody struct {
	TypeVersion string `json:"type_version,omitempty"`
	Disabled    bool   `json:"disabled"`
}

// cloneBody carries the new private operation key.
type cloneBody struct {
	TypeVersion string `json:"type_version,omitempty"`
	NewKey      string `json:"new_type"`
}

// handleOperations lists the effective catalog and accepts a new private
// descriptor.
func (h *Hub) handleOperations(w http.ResponseWriter, r *http.Request) {
	if h.operations == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPERATION_CATALOG_UNAVAILABLE",
			"gateway operation catalog is not enabled")
		return
	}
	switch r.Method {
	case http.MethodGet:
		views, err := h.operations.Effective(r.Context(), r.PathValue("tenant"))
		if err != nil {
			writeOperationError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, map[string]any{"items": views})
	case http.MethodPost:
		var body operationBody
		if !decodeBody(w, r, &body) {
			return
		}
		d, err := body.toDescriptor()
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "OPERATION_INVALID", err.Error())
			return
		}
		if err := h.operations.CreateManual(r.Context(), r.PathValue("tenant"), d); err != nil {
			writeOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGatewayOperations lists one gateway's own operation catalog: the
// descriptors that gateway advertised plus tenant-private fallback rows.
func (h *Hub) handleGatewayOperations(w http.ResponseWriter, r *http.Request) {
	if h.operations == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPERATION_CATALOG_UNAVAILABLE",
			"gateway operation catalog is not enabled")
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	views, err := h.operations.ListForGateway(r.Context(),
		r.PathValue("tenant"), r.PathValue("gateway"))
	if err != nil {
		writeOperationError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": views})
}

// handleOperation serves GET/PUT/DELETE one private descriptor, POST clone,
// and PUT on the disabled sub-resource.
func (h *Hub) handleOperation(w http.ResponseWriter, r *http.Request) {
	if h.operations == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPERATION_CATALOG_UNAVAILABLE",
			"gateway operation catalog is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	operation := r.PathValue("operation")
	version := r.URL.Query().Get("version")

	switch r.Method {
	case http.MethodGet:
		views, err := h.operations.Effective(r.Context(), tenant)
		if err != nil {
			writeOperationError(w, err)
			return
		}
		for _, view := range views {
			if view.Operation == operation && view.TypeVersion == version {
				writeRESTJSON(w, http.StatusOK, view)
				return
			}
		}
		writeRESTError(w, http.StatusNotFound, "OPERATION_NOT_FOUND",
			"gateway operation not found")
	case http.MethodPut:
		var body operationBody
		if !decodeBody(w, r, &body) {
			return
		}
		d, err := body.toDescriptor()
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "OPERATION_INVALID", err.Error())
			return
		}
		if err := h.operations.UpdateManual(r.Context(), tenant, operation, version, d); err != nil {
			writeOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := h.operations.DeleteManual(r.Context(), tenant, operation, version); err != nil {
			writeOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		var body cloneBody
		if !decodeBody(w, r, &body) {
			return
		}
		if err := h.operations.CloneToPrivate(r.Context(), tenant,
			operation, body.TypeVersion, body.NewKey); err != nil {
			writeOperationError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleOperationDisabled toggles one operation key for a tenant.
func (h *Hub) handleOperationDisabled(w http.ResponseWriter, r *http.Request) {
	if h.operations == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPERATION_CATALOG_UNAVAILABLE",
			"gateway operation catalog is not enabled")
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body disabledBody
	if !decodeBody(w, r, &body) {
		return
	}
	tenant := r.PathValue("tenant")
	operation := r.PathValue("operation")
	principal := contextPrincipal(r.Context())
	userID := ""
	if principal != nil {
		userID = principal.UserID
	}
	var err error
	if body.Disabled {
		err = h.operations.SetDisabled(r.Context(), tenant, operation, body.TypeVersion, userID)
	} else {
		err = h.operations.ClearDisabled(r.Context(), tenant, operation, body.TypeVersion)
	}
	if err != nil {
		writeOperationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeOperationError maps catalog errors to the REST envelope.
func writeOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, opcatalog.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "OPERATION_NOT_FOUND",
			"gateway operation not found")
	case errors.Is(err, opcatalog.ErrConflict):
		writeRESTError(w, http.StatusConflict, "OPERATION_CONFLICT",
			"a private operation with that key already exists")
	case errors.Is(err, opcatalog.ErrInvalidDescriptor):
		writeRESTError(w, http.StatusBadRequest, "OPERATION_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "OPERATION_FAILED", err.Error())
	}
}
