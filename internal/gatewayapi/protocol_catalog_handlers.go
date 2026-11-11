// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// protocol_catalog_handlers.go exposes the global protocol/device-service
// catalog over tenant-scoped REST (ADR-0033). The catalog rows are global
// vendor facts; the {tenant} path segment exists only to attach the normal
// tenant RBAC gate (profile read/list, profile write for create/update/
// delete). Reads map to profile:read and mutations to profile:write in
// authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/protocolcatalog"
)

// ProtocolCatalogAdmin is the catalog lifecycle surface the handlers need.
type ProtocolCatalogAdmin interface {
	List(ctx context.Context, q string) ([]protocolcatalog.Family, error)
	Get(ctx context.Context, family string) (protocolcatalog.Family, error)
	Create(ctx context.Context, in protocolcatalog.SaveInput) (protocolcatalog.Family, error)
	Update(ctx context.Context, family string, in protocolcatalog.SaveInput) (protocolcatalog.Family, error)
	Delete(ctx context.Context, family string) error
}

// SetProtocolCatalog wires the global protocol-family catalog service.
func (h *Hub) SetProtocolCatalog(catalog ProtocolCatalogAdmin) {
	h.protocolCatalog = catalog
}

// handleProtocolFamilies lists (GET, optional ?q=) or creates (POST) catalog
// families.
func (h *Hub) handleProtocolFamilies(w http.ResponseWriter, r *http.Request) {
	if h.protocolCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROTOCOL_CATALOG_UNAVAILABLE",
			"protocol catalog is not enabled")
		return
	}
	switch r.Method {
	case http.MethodGet:
		families, err := h.protocolCatalog.List(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			writeProtocolCatalogError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, families)
	case http.MethodPost:
		var input protocolcatalog.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		family, err := h.protocolCatalog.Create(r.Context(), input)
		if err != nil {
			writeProtocolCatalogError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, family)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleProtocolFamily operates on one family by key (GET/PUT/DELETE).
func (h *Hub) handleProtocolFamily(w http.ResponseWriter, r *http.Request) {
	if h.protocolCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROTOCOL_CATALOG_UNAVAILABLE",
			"protocol catalog is not enabled")
		return
	}
	family := r.PathValue("family")
	switch r.Method {
	case http.MethodGet:
		got, err := h.protocolCatalog.Get(r.Context(), family)
		if err != nil {
			writeProtocolCatalogError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, got)
	case http.MethodPut:
		var input protocolcatalog.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		updated, err := h.protocolCatalog.Update(r.Context(), family, input)
		if err != nil {
			writeProtocolCatalogError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := h.protocolCatalog.Delete(r.Context(), family); err != nil {
			writeProtocolCatalogError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// writeProtocolCatalogError maps catalog errors to HTTP status codes.
func writeProtocolCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, protocolcatalog.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "PROTOCOL_FAMILY_NOT_FOUND", err.Error())
	case errors.Is(err, protocolcatalog.ErrConflict):
		writeRESTError(w, http.StatusConflict, "PROTOCOL_FAMILY_CONFLICT", err.Error())
	case errors.Is(err, protocolcatalog.ErrBuiltin):
		writeRESTError(w, http.StatusConflict, "PROTOCOL_FAMILY_BUILTIN", err.Error())
	case errors.Is(err, protocolcatalog.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "PROTOCOL_FAMILY_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "PROTOCOL_CATALOG_FAILED", err.Error())
	}
}
