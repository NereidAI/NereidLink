// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ota_handlers.go exposes firmware OTA tasks (ADR-0026). Creating a task
// returns 202 + task id and dispatches asynchronously; progress is read by
// task/item GET or pushed over the ota_task WebSocket resource.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/ota"
)

// OTAService creates and queries firmware tasks.
type OTAService interface {
	Create(ctx context.Context, tenantID, actor string, request ota.Request) (ota.Task, error)
	List(ctx context.Context, tenantID, state string, page, pageSize int) (apipage.Page[ota.Task], error)
	Get(ctx context.Context, tenantID, taskID string) (ota.Task, error)
	ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[ota.Item], error)
	ListItemsByDevice(ctx context.Context, tenantID, deviceID string, page, pageSize int) (apipage.Page[ota.DeviceItem], error)
	ListEvents(ctx context.Context, tenantID, taskID, deviceID string, limit int) ([]ota.TimelineEvent, error)
	Cancel(ctx context.Context, tenantID, taskID, actor string) (ota.Task, int, error)
	Pause(ctx context.Context, tenantID, taskID, actor string) (ota.Task, error)
	Resume(ctx context.Context, tenantID, taskID, actor string) (ota.Task, error)
}

// otaRequestBody is the firmware task request. When firmware_id is set the
// task is platform-hosted and package is ignored; otherwise the legacy
// external-URL package descriptor is required.
type otaRequestBody struct {
	Devices       []string           `json:"devices"`
	Selector      ota.TargetSelector `json:"selector"`
	Package       ota.Package        `json:"package"`
	FirmwareID    string             `json:"firmware_id,omitempty"`
	SuccessPolicy string             `json:"success_policy,omitempty"`
	Policy        ota.RolloutPolicy  `json:"policy,omitempty"`
}

// handleCreateOTA starts an asynchronous firmware task.
func (h *Hub) handleCreateOTA(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	var body otaRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	actor := otaActor(r)
	task, err := h.ota.Create(r.Context(), r.PathValue("tenant"), actor, ota.Request{
		Devices:       body.Devices,
		Selector:      body.Selector,
		Package:       body.Package,
		FirmwareID:    body.FirmwareID,
		SuccessPolicy: body.SuccessPolicy,
		Policy:        body.Policy,
	})
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusAccepted, task)
}

// handleListOTAs returns a page of tenant OTA tasks.
func (h *Hub) handleListOTAs(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	page, pageSize := parsePagination(r)
	result, err := h.ota.List(r.Context(), r.PathValue("tenant"),
		strings.TrimSpace(r.URL.Query().Get("state")), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "OTA_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleGetOTA returns one OTA task.
func (h *Hub) handleGetOTA(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	task, err := h.ota.Get(r.Context(), r.PathValue("tenant"), r.PathValue("task"))
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// handleListDeviceOTAItems returns one page of this device's rollout rows,
// newest first, joined to each parent task.
func (h *Hub) handleListDeviceOTAItems(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	if !h.gateShadowDevice(w, r) {
		return
	}
	page, pageSize := parsePagination(r)
	result, err := h.ota.ListItemsByDevice(r.Context(), r.PathValue("tenant"),
		r.PathValue("device"), page, pageSize)
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleListOTAItems returns one page of per-device OTA progress.
func (h *Hub) handleListOTAItems(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	page, pageSize := parsePagination(r)
	result, err := h.ota.ListItems(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), page, pageSize)
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleListOTAEvents returns the append-only per-device phase timeline of a
// task, optionally narrowed with ?device_id=.
func (h *Hub) handleListOTAEvents(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
	events, err := h.ota.ListEvents(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), deviceID, 500)
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleCancelOTA applies the safe best-effort cancel of a rollout.
func (h *Hub) handleCancelOTA(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	task, stillApplying, err := h.ota.Cancel(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), otaActor(r))
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"task":                 task,
		"still_applying_count": stillApplying,
	})
}

// handlePauseOTA holds a running rollout: no new batches open, applying
// devices continue undisturbed (ADR-0006d §5).
func (h *Hub) handlePauseOTA(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	task, err := h.ota.Pause(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), otaActor(r))
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// handleResumeOTA releases a held rollout and dispatches its open batch.
func (h *Hub) handleResumeOTA(w http.ResponseWriter, r *http.Request) {
	if h.ota == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OTA_SERVICE_UNAVAILABLE",
			"ota service is not enabled")
		return
	}
	task, err := h.ota.Resume(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), otaActor(r))
	if err != nil {
		h.writeOTAError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// writeOTAError maps OTA domain errors to REST statuses.
func (h *Hub) writeOTAError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ota.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "OTA_NOT_FOUND", "ota task not found")
	case errors.Is(err, ota.ErrNotCancellable):
		writeRESTError(w, http.StatusConflict, "OTA_NOT_CANCELLABLE", "ota task is already finished")
	case errors.Is(err, ota.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "OTA_INVALID", err.Error())
	case errors.Is(err, ota.ErrTooManyDevices):
		writeRESTError(w, http.StatusBadRequest, "OTA_TOO_MANY_DEVICES", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "OTA_FAILED", err.Error())
	}
}

// otaActor returns the principal id used for the created_by audit column.
func otaActor(r *http.Request) string {
	if p := contextPrincipal(r.Context()); p != nil && p.UserID != "" {
		return p.UserID
	}
	return "anonymous"
}

// SetOTA wires firmware task handling.
func (h *Hub) SetOTA(service OTAService) {
	h.ota = service
}
