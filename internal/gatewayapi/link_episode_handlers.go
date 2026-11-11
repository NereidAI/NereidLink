// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// link_episode_handlers.go exposes the read-only link episode history
// for the gateway detail Link Health tab. Episodes enter the platform
// over the MQTT linkhealth uplink; this handler only lists them.

package gatewayapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/linknotify"
)

// LinkEpisodeReader is the read surface over the link episode ledger.
type LinkEpisodeReader interface {
	// ListEpisodes returns one page of link episode rows.
	ListEpisodes(ctx context.Context, tenantID string, filter linknotify.EpisodeFilter, page, pageSize int) (apipage.Page[linknotify.EpisodeRow], error)
}

// SetLinkEpisodeReader wires the link episode history service.
func (h *Hub) SetLinkEpisodeReader(reader LinkEpisodeReader) {
	h.linkEpisodeReader = reader
}

// handleLinkEpisodes returns one page of link episode rows.
func (h *Hub) handleLinkEpisodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.linkEpisodeReader == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "LINK_EPISODE_SERVICE_UNAVAILABLE",
			"link episode history is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()
	filter := linknotify.EpisodeFilter{
		GatewayID: query.Get("gateway_id"),
		Source:    query.Get("source"),
	}
	if raw := query.Get("start"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.Start = parsed
		}
	}
	if raw := query.Get("end"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.End = parsed
		}
	}

	result, err := h.linkEpisodeReader.ListEpisodes(r.Context(),
		r.PathValue("tenant"), filter, page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "LINK_EPISODE_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}
