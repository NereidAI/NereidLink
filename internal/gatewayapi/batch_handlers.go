// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batch_handlers.go exposes asynchronous batch command tasks and their
// per-device items.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/command"
)

// BatchCommandService creates and queries batch command tasks.
type BatchCommandService interface {
	Create(ctx context.Context, tenantID, createdBy string, request command.BatchRequest) (command.Task, error)
	List(ctx context.Context, tenantID string, page, pageSize int) (apipage.Page[command.Task], error)
	Get(ctx context.Context, tenantID, taskID string) (command.Task, error)
	ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[command.TaskItem], error)
}

// batchRequestBody is the batch command request.
type batchRequestBody struct {
	Name      string          `json:"name"`
	Direction string          `json:"direction,omitempty"`
	Params    json.RawMessage `json:"params"`
	TimeoutMS int64           `json:"timeout_ms"`
	GroupID   string          `json:"group_id"`
	DeviceIDs []string        `json:"device_ids"`
}

// handleCreateBatchCommand expands and starts an asynchronous task.
func (h *Hub) handleCreateBatchCommand(w http.ResponseWriter, r *http.Request) {
	if h.batchCommands == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "BATCH_COMMAND_SERVICE_UNAVAILABLE",
			"batch command service is not enabled")
		return
	}
	var body batchRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}

	principal := contextPrincipal(r.Context())
	createdBy := ""
	if principal != nil {
		createdBy = principal.UserID
	}
	task, err := h.batchCommands.Create(r.Context(), r.PathValue("tenant"), createdBy,
		command.BatchRequest{
			Name:      body.Name,
			Direction: body.Direction,
			Params:    body.Params,
			TimeoutMS: body.TimeoutMS,
			GroupID:   body.GroupID,
			DeviceIDs: body.DeviceIDs,
		})
	if err != nil {
		switch {
		case errors.Is(err, command.ErrInvalidArgument):
			writeRESTError(w, http.StatusBadRequest, "BATCH_COMMAND_INVALID", err.Error())
		case errors.Is(err, command.ErrBodyTooLarge):
			writeRESTError(w, http.StatusRequestEntityTooLarge, "COMMAND_BODY_TOO_LARGE",
				"parameter document exceeds the configured limit")
		default:
			writeRESTError(w, http.StatusInternalServerError, "BATCH_COMMAND_FAILED", err.Error())
		}
		return
	}
	writeRESTJSON(w, http.StatusAccepted, task)
}

// handleListBatchCommands returns tenant batch tasks.
func (h *Hub) handleListBatchCommands(w http.ResponseWriter, r *http.Request) {
	page, pageSize := parsePagination(r)
	result, err := h.batchCommands.List(r.Context(), r.PathValue("tenant"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "BATCH_COMMAND_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleGetBatchCommand returns one task.
func (h *Hub) handleGetBatchCommand(w http.ResponseWriter, r *http.Request) {
	task, err := h.batchCommands.Get(r.Context(), r.PathValue("tenant"), r.PathValue("task"))
	if err != nil {
		h.writeBatchError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// handleListBatchItems returns one page of per-device results.
func (h *Hub) handleListBatchItems(w http.ResponseWriter, r *http.Request) {
	page, pageSize := parsePagination(r)
	result, err := h.batchCommands.ListItems(r.Context(), r.PathValue("tenant"),
		r.PathValue("task"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "BATCH_ITEM_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// writeBatchError maps task lookup failures.
func (h *Hub) writeBatchError(w http.ResponseWriter, err error) {
	if errors.Is(err, command.ErrNotFound) {
		writeRESTError(w, http.StatusNotFound, "BATCH_COMMAND_NOT_FOUND", "batch command task not found")
		return
	}
	writeRESTError(w, http.StatusInternalServerError, "BATCH_COMMAND_FAILED", err.Error())
}

// SetBatchCommands wires batch command handling.
func (h *Hub) SetBatchCommands(service BatchCommandService) {
	h.batchCommands = service
}
