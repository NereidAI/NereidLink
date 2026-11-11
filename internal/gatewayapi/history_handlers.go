// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// history_handlers.go exposes telemetry history and command history for the
// device detail page, using the shared pagination envelope.

package gatewayapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/presence"
	"github.com/nereidai/nereidlink/internal/telemetry"
)

// TelemetryHistory is the historical telemetry lookup surface.
type TelemetryHistory interface {
	History(ctx context.Context, tenantID, deviceID, key string, start, end time.Time, page, pageSize int) (apipage.Page[telemetry.HistoryPoint], error)
}

// TelemetryKeys is the measurement discovery surface.
type TelemetryKeys interface {
	Keys(ctx context.Context, tenantID, deviceID string, start, end time.Time) ([]telemetry.KeySummary, error)
}

// TelemetrySeries is the downsampled series lookup surface.
type TelemetrySeries interface {
	Series(ctx context.Context, tenantID, deviceID, key string, start, end time.Time) ([]telemetry.SeriesPoint, error)
}

// PresenceLister lists the online devices of a tenant.
type PresenceLister interface {
	ListOnline(ctx context.Context, tenantID string) ([]presence.OnlineDevice, error)
}

// CommandHistory is the command history lookup surface.
type CommandHistory interface {
	History(ctx context.Context, tenantID, deviceID, state string, page, pageSize int) (apipage.Page[command.Record], error)
	ListTenant(ctx context.Context, tenantID, deviceID, name, state string, page, pageSize int) (apipage.Page[command.Record], error)
}

// defaultHistoryWindow is applied when start/end are both absent.
const defaultHistoryWindow = 24 * time.Hour

// defaultKeysWindow bounds measurement discovery: keys silent for over
// 30 days are no longer offered.
const defaultKeysWindow = 30 * 24 * time.Hour

// Per-endpoint maximum requestable spans (ADR-0017 query guards). The raw
// history page returns individual rows, so it is the tightest; long trends
// must read the downsampled series, which is bounded by bucket count rather
// than rows and may span roughly a year over the daily rollup.
const (
	maxHistoryWindow = 7 * 24 * time.Hour
	maxKeysWindow    = 90 * 24 * time.Hour
	maxSeriesWindow  = 400 * 24 * time.Hour
)

// SetTelemetryHistory wires the telemetry history source.
func (h *Hub) SetTelemetryHistory(history TelemetryHistory) {
	h.telemetryHistory = history
}

// SetTelemetryKeys wires the measurement discovery source.
func (h *Hub) SetTelemetryKeys(keys TelemetryKeys) {
	h.telemetryKeys = keys
}

// SetPresenceLister wires the presence snapshot source.
func (h *Hub) SetPresenceLister(lister PresenceLister) {
	h.presenceLister = lister
}

// SetTelemetrySeries wires the downsampled series source.
func (h *Hub) SetTelemetrySeries(series TelemetrySeries) {
	h.telemetrySeries = series
}

// SetCommandHistory wires the command history source.
func (h *Hub) SetCommandHistory(history CommandHistory) {
	h.commandHistory = history
}

// handleTelemetryHistory returns a page of stored telemetry. The time
// window defaults to the last 24 hours; explicit start/end must be RFC3339.
func (h *Hub) handleTelemetryHistory(w http.ResponseWriter, r *http.Request) {
	if h.telemetryHistory == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "TELEMETRY_SERVICE_UNAVAILABLE",
			"telemetry history is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

	start, end, err := parseTimeWindow(query, defaultHistoryWindow, maxHistoryWindow)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_TIME_WINDOW", err.Error())
		return
	}

	result, err := h.telemetryHistory.History(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		query.Get("key"), start, end, page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TELEMETRY_HISTORY_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleTelemetryKeys returns the latest value of every measurement key
// reported within the window. It backs the measurement selector and the
// latest-telemetry strip.
func (h *Hub) handleTelemetryKeys(w http.ResponseWriter, r *http.Request) {
	if h.telemetryKeys == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "TELEMETRY_SERVICE_UNAVAILABLE",
			"telemetry keys are not enabled")
		return
	}

	query := r.URL.Query()
	start, end, err := parseTimeWindow(query, defaultKeysWindow, maxKeysWindow)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_TIME_WINDOW", err.Error())
		return
	}

	result, err := h.telemetryKeys.Keys(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), start, end)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TELEMETRY_KEYS_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// presenceSnapshot is the online device listing of one tenant.
type presenceSnapshot struct {
	// Devices lists every online device.
	Devices []presenceSnapshotDevice `json:"devices"`
}

// presenceSnapshotDevice is one online device entry.
type presenceSnapshotDevice struct {
	// DeviceID is the online device identifier.
	DeviceID string `json:"device_id"`

	// IngressID owns the live transport session.
	IngressID string `json:"ingress_id"`

	// SessionID is the fencing token of the live session.
	SessionID string `json:"session_id"`

	// GatewayID is set when attached through a gateway.
	GatewayID string `json:"gateway_id,omitempty"`
}

// handleTelemetrySeries returns downsampled values covering the window.
// The bucket width is chosen server-side so any range spans at most a few
// hundred buckets.
func (h *Hub) handleTelemetrySeries(w http.ResponseWriter, r *http.Request) {
	if h.telemetrySeries == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "TELEMETRY_SERVICE_UNAVAILABLE",
			"telemetry series is not enabled")
		return
	}

	query := r.URL.Query()
	start, end, err := parseTimeWindow(query, defaultHistoryWindow, maxSeriesWindow)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_TIME_WINDOW", err.Error())
		return
	}

	result, err := h.telemetrySeries.Series(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		query.Get("key"), start, end)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TELEMETRY_SERIES_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handlePresenceSnapshot returns every online device so a freshly loaded
// client can seed its presence map without waiting for the next boundary
// event.
func (h *Hub) handlePresenceSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.presenceLister == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PRESENCE_UNAVAILABLE",
			"presence snapshot is not enabled")
		return
	}
	devices, err := h.presenceLister.ListOnline(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "PRESENCE_SNAPSHOT_FAILED", err.Error())
		return
	}
	snapshot := presenceSnapshot{Devices: make([]presenceSnapshotDevice, 0, len(devices))}
	for _, device := range devices {
		snapshot.Devices = append(snapshot.Devices, presenceSnapshotDevice{
			DeviceID:  device.DeviceID,
			IngressID: device.IngressID,
			SessionID: device.SessionID,
			GatewayID: device.GatewayID,
		})
	}
	writeRESTJSON(w, http.StatusOK, snapshot)
}

// handleCommandHistory returns a page of command audit rows for a device.
func (h *Hub) handleCommandHistory(w http.ResponseWriter, r *http.Request) {
	if h.commandHistory == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command history is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	result, err := h.commandHistory.History(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		r.URL.Query().Get("state"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "COMMAND_HISTORY_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleTenantCommands returns one page of commands across every device of
// a tenant for the command center.
func (h *Hub) handleTenantCommands(w http.ResponseWriter, r *http.Request) {
	if h.commandHistory == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command history is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()
	result, err := h.commandHistory.ListTenant(r.Context(),
		r.PathValue("tenant"), query.Get("device_id"),
		query.Get("name"), query.Get("state"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "COMMAND_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}
