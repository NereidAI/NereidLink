// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// model_inventory_handlers.go exposes the edge model inventory over tenant
// REST (ADR-0039). One read-only collection returns the models a gateway
// last reported in the full-replacement manifest "models" segment, including
// state/reason/labels and the stale marker. The route reuses connector:read
// (see authorize.go). The binding wizard uses this to present only READY,
// non-stale models before creating an edge-inference connector.

package gatewayapi

import (
	"context"
	"net/http"

	"github.com/nereidai/nereidlink/internal/modelinventory"
)

// ModelInventoryReader lists a gateway's reported model inventory.
type ModelInventoryReader interface {
	ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]modelinventory.Model, error)
}

// SetModelInventoryReader wires the edge model inventory service.
func (h *Hub) SetModelInventoryReader(reader ModelInventoryReader) {
	h.modelInventory = reader
}

// handleGatewayModels lists one gateway's model inventory.
//
//	GET /v1/tenants/{tenant}/gateways/{gateway}/models
//
// Empty inventory is "[]", never null.
func (h *Hub) handleGatewayModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.modelInventory == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "MODEL_INVENTORY_UNAVAILABLE",
			"edge model inventory is not enabled")
		return
	}
	tenant, gateway := r.PathValue("tenant"), r.PathValue("gateway")
	models, err := h.modelInventory.ListForGateway(r.Context(), tenant, gateway)
	if err != nil {
		writeModelInventoryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, models)
}

// writeModelInventoryError maps inventory errors to HTTP status codes.
func writeModelInventoryError(w http.ResponseWriter, err error) {
	writeRESTError(w, http.StatusInternalServerError, "MODEL_INVENTORY_FAILED", err.Error())
}
