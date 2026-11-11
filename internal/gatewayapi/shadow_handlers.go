// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_handlers.go exposes device shadow state over tenant REST.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/shadow"
)

// ShadowStore is the shadow management surface.
type ShadowStore interface {
	// Get returns the current shadow.
	Get(ctx context.Context, deviceID string) (shadow.State, error)

	// UpdateDesired applies a platform desired-state patch.
	UpdateDesired(ctx context.Context, deviceID string, patch shadow.DesiredPatch) (shadow.State, error)

	// UpdateReported applies a device report.
	UpdateReported(ctx context.Context, deviceID string, patch shadow.ReportedPatch) (shadow.State, error)
}

// SetShadowStore wires the shadow service.
func (h *Hub) SetShadowStore(store ShadowStore) {
	h.shadowStore = store
}

// gateShadowDevice enforces tenant ownership and the caller's data scope on
// the device before touching the shadow store. The shadow store itself keys
// only by device id (device ingress reports with its own authenticated
// identity), so the tenant REST surface must prove the device is in the named
// tenant and visible; otherwise a crafted id could reach another tenant's
// shadow. An out-of-scope device is reported as not-found, never forbidden.
func (h *Hub) gateShadowDevice(w http.ResponseWriter, r *http.Request) bool {
	if !h.registryReady(w) {
		return false
	}
	if _, err := h.registryAdmin.GetDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device")); err != nil {
		writeRegistryError(w, err)
		return false
	}
	return true
}

func (h *Hub) handleGetShadow(w http.ResponseWriter, r *http.Request) {
	if !h.gateShadowDevice(w, r) {
		return
	}
	state, err := h.shadowStore.Get(r.Context(), r.PathValue("device"))
	if h.writeShadowError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, state)
}

func (h *Hub) handleUpdateDesired(w http.ResponseWriter, r *http.Request) {
	if !h.gateShadowDevice(w, r) {
		return
	}
	var patch shadow.DesiredPatch
	if !decodeBody(w, r, &patch) {
		return
	}
	patch.ExpectedETag = r.Header.Get("If-Match")
	state, err := h.shadowStore.UpdateDesired(r.Context(), r.PathValue("device"), patch)
	if h.writeShadowError(w, err) {
		return
	}
	w.Header().Set("ETag", state.ETag)
	writeRESTJSON(w, http.StatusOK, state)
}

func (h *Hub) handleUpdateReported(w http.ResponseWriter, r *http.Request) {
	if !h.gateShadowDevice(w, r) {
		return
	}
	var patch shadow.ReportedPatch
	if !decodeBody(w, r, &patch) {
		return
	}
	state, err := h.shadowStore.UpdateReported(r.Context(), r.PathValue("device"), patch)
	if h.writeShadowError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, state)
}

func (h *Hub) writeShadowError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, shadow.ErrDeviceNotFound):
		writeRESTError(w, http.StatusNotFound, "SHADOW_DEVICE_NOT_FOUND", err.Error())
	case errors.Is(err, shadow.ErrETagMismatch):
		writeRESTError(w, http.StatusConflict, "SHADOW_ETAG_MISMATCH", err.Error())
	case errors.Is(err, shadow.ErrInvalidPatch):
		writeRESTError(w, http.StatusBadRequest, "SHADOW_INVALID_PATCH", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "SHADOW_OPERATION_FAILED", err.Error())
	}
	return true
}
