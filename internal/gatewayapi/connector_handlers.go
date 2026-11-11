// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// connector_handlers.go exposes edge connector instance desired-spec CRUD
// over tenant REST (ADR-0026). The gateway id is the gateway device UUID;
// routes are mapped to connector:read / connector:write in authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/connectorcatalog"
)

// ConnectorAdmin is the connector lifecycle administration surface.
type ConnectorAdmin interface {
	ListSpecs(ctx context.Context, tenantID, gatewayID string) ([]connector.Spec, error)
	GetSpec(ctx context.Context, tenantID, specID string) (connector.Spec, error)
	CreateSpec(ctx context.Context, tenantID, gatewayID string, in connector.SaveInput) (connector.Spec, error)
	UpdateSpec(ctx context.Context, tenantID, specID string, in connector.SaveInput) (connector.Spec, error)
	DeleteSpec(ctx context.Context, tenantID, specID string) error
	// ReconcileView returns the read-only desired-vs-applied classification
	// for one gateway, computed on demand from the last manifest watermark.
	ReconcileView(ctx context.Context, tenantID, gatewayID string) (connector.ReconcileView, error)
}

// ConnectorSchemaReader lists the edge-registered connector config schemas
// (the catalog that backs schema-driven config forms).
type ConnectorSchemaReader interface {
	ListSchemas(ctx context.Context, tenantID string) ([]connectorcatalog.Schema, error)
}

// SetConnectorSchemaReader wires the connector schema catalog read surface.
func (h *Hub) SetConnectorSchemaReader(reader ConnectorSchemaReader) {
	h.connectorSchemas = reader
}

// handleConnectorSchemas lists every config schema an edge has registered for
// the tenant during hello (ADR-0026 edge-registered schemas).
func (h *Hub) handleConnectorSchemas(w http.ResponseWriter, r *http.Request) {
	if h.connectorSchemas == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "CONNECTOR_CATALOG_UNAVAILABLE",
			"connector schema catalog is not enabled")
		return
	}
	schemas, err := h.connectorSchemas.ListSchemas(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "CONNECTOR_CATALOG_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, schemas)
}

// handleConnectorReconcile returns the read-only drift classification for one
// gateway. It never accepts a manifest or mutates state, so the console can
// poll it. Requires connector:read.
func (h *Hub) handleConnectorReconcile(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.connectorCallReady(w)
	if !ok {
		return
	}
	view, err := admin.ReconcileView(r.Context(),
		r.PathValue("tenant"), r.PathValue("gateway"))
	if err != nil {
		writeConnectorError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, view)
}

// SetConnectorAdmin wires the connector lifecycle service.
func (h *Hub) SetConnectorAdmin(admin ConnectorAdmin) {
	h.connectorAdmin = admin
}

// handleConnectors lists or creates desired specs for one gateway.
func (h *Hub) handleConnectors(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.connectorCallReady(w)
	if !ok {
		return
	}
	tenant, gateway := r.PathValue("tenant"), r.PathValue("gateway")
	switch r.Method {
	case http.MethodGet:
		specs, err := admin.ListSpecs(r.Context(), tenant, gateway)
		if err != nil {
			writeConnectorError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, specs)
	case http.MethodPost:
		var input connector.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		spec, err := admin.CreateSpec(r.Context(), tenant, gateway, input)
		if err != nil {
			writeConnectorError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, spec)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleConnector operates on one spec by id.
func (h *Hub) handleConnector(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.connectorCallReady(w)
	if !ok {
		return
	}
	tenant, spec := r.PathValue("tenant"), r.PathValue("spec")
	switch r.Method {
	case http.MethodGet:
		got, err := admin.GetSpec(r.Context(), tenant, spec)
		if err != nil {
			writeConnectorError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, got)
	case http.MethodPut:
		var input connector.SaveInput
		if !decodeBody(w, r, &input) {
			return
		}
		updated, err := admin.UpdateSpec(r.Context(), tenant, spec, input)
		if err != nil {
			writeConnectorError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := admin.DeleteSpec(r.Context(), tenant, spec); err != nil {
			writeConnectorError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// connectorCallReady verifies the connector service is wired.
func (h *Hub) connectorCallReady(w http.ResponseWriter) (ConnectorAdmin, bool) {
	if h.connectorAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "CONNECTOR_SERVICE_UNAVAILABLE",
			"connector lifecycle management is not enabled")
		return nil, false
	}
	return h.connectorAdmin, true
}

// writeConnectorError maps connector errors to HTTP status codes.
func writeConnectorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, connector.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "CONNECTOR_SPEC_NOT_FOUND", err.Error())
	case errors.Is(err, connector.ErrGatewayNotFound):
		writeRESTError(w, http.StatusNotFound, "GATEWAY_NOT_FOUND", err.Error())
	case errors.Is(err, connector.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "CONNECTOR_SPEC_INVALID", err.Error())
	case errors.Is(err, connector.ErrConflict):
		writeRESTError(w, http.StatusConflict, "CONNECTOR_SPEC_CONFLICT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "CONNECTOR_OPERATION_FAILED", err.Error())
	}
}
