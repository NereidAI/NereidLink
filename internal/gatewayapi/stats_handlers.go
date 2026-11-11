// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stats_handlers.go exposes the statistics center endpoints: the system
// infrastructure snapshot and the tenant business report.

package gatewayapi

import (
	"context"
	"net/http"

	"github.com/nereidai/nereidlink/internal/stats"
)

// StatsReader is the statistics collection surface.
type StatsReader interface {
	// System returns the infrastructure snapshot.
	System(ctx context.Context) (stats.SystemSnapshot, error)

	// Business returns the tenant report for a range preset (1h/24h/7d).
	Business(ctx context.Context, tenantID, rangeKey string) (stats.BusinessReport, error)
}

// handleSystemStats returns the on-demand infrastructure snapshot.
func (h *Hub) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	if h.statsReader == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "STATS_SERVICE_UNAVAILABLE",
			"statistics service is not enabled")
		return
	}

	result, err := h.statsReader.System(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "STATS_SYSTEM_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleBusinessStats returns the tenant business report.
func (h *Hub) handleBusinessStats(w http.ResponseWriter, r *http.Request) {
	if h.statsReader == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "STATS_SERVICE_UNAVAILABLE",
			"statistics service is not enabled")
		return
	}

	result, err := h.statsReader.Business(r.Context(),
		r.PathValue("tenant"), r.URL.Query().Get("range"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "STATS_BUSINESS_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}
