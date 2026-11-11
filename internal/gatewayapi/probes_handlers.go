// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// probes_handlers.go exposes on-demand gateway probe tasks (ADR-0026
// section 9). Creating a probe returns 202 + task id and continues
// asynchronously; results are read by task/item GET or pushed over the
// probe_task WebSocket resource.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/probe"
)

// ProbeService creates and queries on-demand probe tasks.
type ProbeService interface {
	Create(ctx context.Context, tenantID, actor string, request probe.Request) (probe.Task, error)
	List(ctx context.Context, tenantID string, filter probe.TaskFilter, page, pageSize int) (apipage.Page[probe.Task], error)
	Get(ctx context.Context, tenantID, taskID string) (probe.Task, error)
	ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[probe.Item], error)
}

// probeRequestBody is the on-demand probe request.
type probeRequestBody struct {
	Devices   []string `json:"devices"`
	Mode      string   `json:"mode"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// handleCreateProbe starts an asynchronous probe task.
func (h *Hub) handleCreateProbe(w http.ResponseWriter, r *http.Request) {
	if h.probes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROBE_SERVICE_UNAVAILABLE",
			"probe service is not enabled")
		return
	}
	var body probeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	actor := probeActor(r)
	task, err := h.probes.Create(r.Context(), r.PathValue("tenant"), actor, probe.Request{
		Devices:   body.Devices,
		Mode:      body.Mode,
		TimeoutMS: body.TimeoutMS,
	})
	if err != nil {
		h.writeProbeError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusAccepted, task)
}

// handleListProbes returns a page of tenant probe tasks.
func (h *Hub) handleListProbes(w http.ResponseWriter, r *http.Request) {
	if h.probes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROBE_SERVICE_UNAVAILABLE",
			"probe service is not enabled")
		return
	}
	page, pageSize := parsePagination(r)
	query := r.URL.Query()
	filter := probe.TaskFilter{
		State: query.Get("state"),
		Mode:  query.Get("mode"),
		Query: query.Get("q"),
	}
	result, err := h.probes.List(r.Context(), r.PathValue("tenant"), filter, page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "PROBE_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleGetProbe returns one probe task.
func (h *Hub) handleGetProbe(w http.ResponseWriter, r *http.Request) {
	if h.probes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROBE_SERVICE_UNAVAILABLE",
			"probe service is not enabled")
		return
	}
	task, err := h.probes.Get(r.Context(), r.PathValue("tenant"), r.PathValue("task"))
	if err != nil {
		h.writeProbeError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// handleListProbeItems returns one page of per-device probe results.
func (h *Hub) handleListProbeItems(w http.ResponseWriter, r *http.Request) {
	if h.probes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROBE_SERVICE_UNAVAILABLE",
			"probe service is not enabled")
		return
	}
	page, pageSize := parsePagination(r)
	result, err := h.probes.ListItems(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), page, pageSize)
	if err != nil {
		h.writeProbeError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// writeProbeError maps probe domain errors to REST statuses.
func (h *Hub) writeProbeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, probe.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "PROBE_NOT_FOUND", "probe task not found")
	case errors.Is(err, probe.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "PROBE_INVALID", err.Error())
	case errors.Is(err, probe.ErrTooManyDevices):
		writeRESTError(w, http.StatusBadRequest, "PROBE_TOO_MANY_DEVICES", err.Error())
	case errors.Is(err, probe.ErrRateLimited):
		writeRESTError(w, http.StatusTooManyRequests, "PROBE_RATE_LIMITED", err.Error())
	case errors.Is(err, probe.ErrTooManyConcurrent):
		writeRESTError(w, http.StatusTooManyRequests, "PROBE_CONCURRENCY_LIMITED", err.Error())
	case errors.Is(err, probe.ErrCooldown):
		writeRESTError(w, http.StatusTooManyRequests, "PROBE_COOLDOWN", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "PROBE_FAILED", err.Error())
	}
}

// probeActor returns the principal key used for the per-principal QPS and
// concurrency limits. Human and machine tokens both carry a UUID subject.
func probeActor(r *http.Request) string {
	if p := contextPrincipal(r.Context()); p != nil && p.UserID != "" {
		return p.UserID
	}
	return "anonymous"
}

// SetProbes wires on-demand probe handling.
func (h *Hub) SetProbes(service ProbeService) {
	h.probes = service
}
