// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_handler.go implements the device-facing shadow report endpoint.

package ingesthttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/nereidai/nereidlink/internal/shadow"
)

// ShadowReporter accepts reports from authenticated devices.
type ShadowReporter interface {
	// UpdateReported merges one device-reported patch.
	UpdateReported(ctx context.Context, deviceID string, patch shadow.ReportedPatch) (shadow.State, error)
}

func (s *Server) handleShadowReport(w http.ResponseWriter, r *http.Request) {
	if s.shadowReporter == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "SHADOW_UNAVAILABLE",
			"shadow service is not enabled")
		return
	}
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.tune.MaxTelemetryBytes))
	if err != nil {
		writeDeviceError(w, http.StatusBadRequest, "SHADOW_REPORT_READ_FAILED",
			"failed to read shadow report")
		return
	}
	var patch shadow.ReportedPatch
	if err := json.Unmarshal(payload, &patch); err != nil {
		writeDeviceError(w, http.StatusBadRequest, "SHADOW_REPORT_INVALID", err.Error())
		return
	}
	state, err := s.shadowReporter.UpdateReported(r.Context(), identity.DeviceID, patch)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SHADOW_REPORT_FAILED", err.Error())
		return
	}
	writeDeviceJSON(w, http.StatusOK, state)
}
