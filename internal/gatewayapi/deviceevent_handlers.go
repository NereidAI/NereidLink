// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// deviceevent_handlers.go exposes the append-only device domain-event ledger
// (v1/me/events source) to the console, narrowed by the caller's data scope.

package gatewayapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/nereidai/nereidlink/internal/deviceevent"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// DeviceEventReader lists stored device events under a data scope.
type DeviceEventReader interface {
	List(ctx context.Context, q deviceevent.Query, scope rbac.DataScope) ([]deviceevent.Event, error)
}

// SetDeviceEventReader wires the device-event ledger query source.
func (h *Hub) SetDeviceEventReader(reader DeviceEventReader) {
	h.deviceEvents = reader
}

// parseEventWindow resolves the optional start/end RFC3339 query parameters,
// defaulting to the supplied window. Unparseable values are ignored (the
// filter is simply not applied), matching the telemetry history handler.
func parseEventWindow(r *http.Request, fallback time.Duration) (time.Time, time.Time) {
	now := time.Now().UTC()
	start := now.Add(-fallback)
	end := now
	query := r.URL.Query()
	if raw := query.Get("start"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			start = parsed
		}
	}
	if raw := query.Get("end"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			end = parsed
		}
	}
	return start, end
}

// handleDeviceEvents returns device domain events newest first. The route is
// tenant-scoped; optional device_id/event_type/start/end/limit parameters
// narrow the result, and the caller's data scope restricts which devices are
// visible. The response is a plain array ([] when empty, never null).
func (h *Hub) handleDeviceEvents(w http.ResponseWriter, r *http.Request) {
	if h.deviceEvents == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DEVICE_EVENTS_UNAVAILABLE",
			"device events are not enabled")
		return
	}

	query := r.URL.Query()
	start, end := parseEventWindow(r, defaultHistoryWindow)

	// The per-device route carries the id in the path; the tenant-wide route
	// (/device-events) has no path id and instead reads device_id. The path
	// value wins when both are present.
	deviceID := r.PathValue("device")
	if deviceID == "" {
		deviceID = query.Get("device_id")
	}

	limit := 0
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	result, err := h.deviceEvents.List(r.Context(), deviceevent.Query{
		TenantID:  r.PathValue("tenant"),
		DeviceID:  deviceID,
		EventType: query.Get("event_type"),
		From:      start,
		To:        end,
		Limit:     limit,
	}, rbac.ScopeFromContext(r.Context()))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "DEVICE_EVENTS_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}
