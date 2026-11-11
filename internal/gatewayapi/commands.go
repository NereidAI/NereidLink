// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/command"
)

// CommandSender dispatches one validated command. It is implemented by
// *command.API.
type CommandSender interface {
	Send(ctx context.Context, tenantID, deviceID, name, direction string, params []byte, timeoutMS int64) (command.Ack, error)
}

// commandRequestBody is the REST command request document.
type commandRequestBody struct {
	// Name identifies the catalog entry.
	Name string `json:"name"`

	// Direction selects read or write. Empty lets the catalog default.
	Direction string `json:"direction,omitempty"`

	// Params is the optional JSON parameter document.
	Params json.RawMessage `json:"params"`

	// TimeoutMS optionally overrides the catalog default timeout.
	TimeoutMS int64 `json:"timeout_ms"`
}

// commandResponseBody is the synchronous command result.
type commandResponseBody struct {
	// CommandID is the generated command identifier.
	CommandID string `json:"command_id"`

	// Status is the synchronous lifecycle status.
	Status string `json:"status"`

	// Response carries the raw JSON device response when present.
	Response json.RawMessage `json:"response,omitempty"`

	// Error carries the machine-readable failure detail.
	Error string `json:"error,omitempty"`
}

// handleSendCommand validates and dispatches one command. The synchronous
// response is QUEUED for an accepted persistent command created offline and
// the terminal status otherwise.
func (h *Hub) handleSendCommand(w http.ResponseWriter, r *http.Request) {
	if h.commands == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command service is not enabled")
		return
	}

	var body commandRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	if body.Name == "" {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "name is required")
		return
	}

	params := body.Params
	if len(params) == 0 {
		params = []byte("{}")
	}

	ack, err := h.commands.Send(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		body.Name, body.Direction, params, body.TimeoutMS)
	if err != nil {
		switch {
		case errors.Is(err, command.ErrNotFound):
			writeRESTError(w, http.StatusNotFound, "COMMAND_NOT_FOUND",
				"command is not declared in the catalog")
		case errors.Is(err, command.ErrDirectionDenied):
			writeRESTError(w, http.StatusBadRequest, "COMMAND_DIRECTION_DENIED",
				"requested read/write direction is not allowed for this command")
		case errors.Is(err, command.ErrDeviceInactive):
			writeRESTError(w, http.StatusConflict, "DEVICE_INACTIVE",
				"device is unclaimed, decommissioned or archived")
		case errors.Is(err, command.ErrBodyTooLarge):
			writeRESTError(w, http.StatusRequestEntityTooLarge, "COMMAND_BODY_TOO_LARGE",
				"parameter document exceeds the configured limit")
		default:
			writeRESTError(w, http.StatusInternalServerError, "COMMAND_DISPATCH_FAILED",
				err.Error())
		}
		return
	}

	writeRESTJSON(w, http.StatusOK, commandResponseBody{
		CommandID: ack.ID,
		Status:    ack.Status,
		Response:  ack.Response,
		Error:     ack.Error,
	})

	// Audit the dispatch with the authenticated actor. Audit failure is
	// logged but never hides the already accepted command response.
	if h.auditReader != nil {
		p := contextPrincipal(r.Context())
		event := audit.Input{
			TenantID:     r.PathValue("tenant"),
			EventType:    audit.EventCommand,
			Action:       "dispatch",
			ResourceType: "device_command",
			ResourceID:   ack.ID,
			Details: map[string]any{
				"device": r.PathValue("device"),
				"name":   body.Name,
				"status": ack.Status,
			},
			IP: requestIP(r),
		}
		if p != nil {
			event.ActorID = p.UserID
			event.ActorName = p.Email
		}
		if _, err := h.auditReader.Append(r.Context(), event); err != nil {
			slog.Error("failed to audit command dispatch", "error", err)
		}
	}
}

// writeRESTJSON writes a JSON response with the given status code.
func writeRESTJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeRESTError writes the global error envelope shape. The trace ID is
// taken from the trace-carrying response writer when the trace middleware
// is active, so every error response can be correlated with server logs
// (ADR-0017).
func writeRESTError(w http.ResponseWriter, status int, code, message string) {
	writeRESTJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":     code,
			"message":  message,
			"trace_id": responseTraceID(w),
		},
	})
}
