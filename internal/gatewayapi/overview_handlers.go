// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// overview_handlers.go exposes the aggregated tenant snapshot consumed by
// the Overview dashboard.

package gatewayapi

import (
	"context"
	"net/http"

	"github.com/nereidai/nereidlink/internal/dashboard"
)

// OverviewReader is the tenant dashboard aggregation surface.
type OverviewReader interface {
	// Summary returns one operational snapshot for a tenant.
	Summary(ctx context.Context, tenantID string) (dashboard.Summary, error)
}

// SetOverviewReader wires the dashboard aggregation service.
func (h *Hub) SetOverviewReader(reader OverviewReader) {
	h.overviewReader = reader
}

// handleOverview returns the aggregated tenant dashboard snapshot.
func (h *Hub) handleOverview(w http.ResponseWriter, r *http.Request) {
	if h.overviewReader == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OVERVIEW_SERVICE_UNAVAILABLE",
			"overview aggregation service is not enabled")
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	summary, err := h.overviewReader.Summary(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "OVERVIEW_SUMMARY_FAILED",
			err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, summary)
}
