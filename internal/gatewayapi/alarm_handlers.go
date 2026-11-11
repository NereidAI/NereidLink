// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// alarm_handlers.go exposes the paged alarm center list and alarm
// acknowledgment used by the Alarms page. The list supports device, state
// (including the compound LIVE value) and severity filters.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/apipage"
)

// AlarmAdmin is the alarm center management surface.
type AlarmAdmin interface {
	// ListPage returns one page of alarm audit rows.
	ListPage(ctx context.Context, tenantID string, filter alarm.Filter, page, pageSize int) (apipage.Page[alarm.Row], error)

	// Ack acknowledges one ACTIVE alarm.
	Ack(ctx context.Context, tenantID, alarmID string) error

	// Assign assigns one alarm or clears its assignment.
	Assign(ctx context.Context, tenantID, alarmID, assigneeID string) error

	// AddComment appends one text comment.
	AddComment(ctx context.Context, tenantID, alarmID, authorID, body string) (alarm.CommentRow, error)

	// ListComments returns the comment timeline.
	ListComments(ctx context.Context, tenantID, alarmID string) ([]alarm.CommentRow, error)
}

// SetAlarmAdmin wires the alarm center service.
func (h *Hub) SetAlarmAdmin(admin AlarmAdmin) {
	h.alarmAdmin = admin
}

// handleAlarms returns one page of alarm rows.
func (h *Hub) handleAlarms(w http.ResponseWriter, r *http.Request) {
	if h.alarmAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "ALARM_SERVICE_UNAVAILABLE",
			"alarm service is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()
	filter := alarm.Filter{
		DeviceID:   query.Get("device_id"),
		AlarmType:  query.Get("alarm_type"),
		AssigneeID: query.Get("assignee"),
		State:      query.Get("state"),
		Severity:   query.Get("severity"),
	}
	result, err := h.alarmAdmin.ListPage(r.Context(),
		r.PathValue("tenant"), filter, page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "ALARM_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleAckAlarm acknowledges one active alarm.
func (h *Hub) handleAckAlarm(w http.ResponseWriter, r *http.Request) {
	if h.alarmAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "ALARM_SERVICE_UNAVAILABLE",
			"alarm service is not enabled")
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := h.alarmAdmin.Ack(r.Context(),
		r.PathValue("tenant"), r.PathValue("alarm")); err != nil {
		if errors.Is(err, alarm.ErrNotFound) {
			writeRESTError(w, http.StatusNotFound, "ALARM_NOT_FOUND", err.Error())
			return
		}
		writeRESTError(w, http.StatusInternalServerError, "ALARM_ACK_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlarmAssign assigns one alarm to a tenant member or clears its
// assignment when the body supplies an empty identifier.
func (h *Hub) handleAlarmAssign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		AssigneeID string `json:"assignee_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "request body must be valid JSON")
		return
	}

	if err := h.alarmAdmin.Assign(r.Context(),
		r.PathValue("tenant"), r.PathValue("alarm"), body.AssigneeID); err != nil {
		switch {
		case errors.Is(err, alarm.ErrNotFound):
			writeRESTError(w, http.StatusNotFound, "ALARM_NOT_FOUND", err.Error())
		case errors.Is(err, alarm.ErrInvalidAssignee):
			writeRESTError(w, http.StatusBadRequest, "INVALID_ASSIGNEE", err.Error())
		default:
			writeRESTError(w, http.StatusInternalServerError, "ALARM_ASSIGN_FAILED", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlarmComments serves or appends the alarm comment timeline.
func (h *Hub) handleAlarmComments(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant")
	alarmID := r.PathValue("alarm")

	switch r.Method {
	case http.MethodGet:
		comments, err := h.alarmAdmin.ListComments(r.Context(), tenantID, alarmID)
		if err != nil {
			writeRESTError(w, http.StatusInternalServerError, "ALARM_COMMENT_LIST_FAILED", err.Error())
			return
		}
		writeRESTJSON(w, http.StatusOK, comments)
	case http.MethodPost:
		principal := contextPrincipal(r.Context())
		if principal == nil {
			writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
			return
		}

		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "request body must be valid JSON")
			return
		}

		comment, err := h.alarmAdmin.AddComment(r.Context(),
			tenantID, alarmID, principal.UserID, body.Body)
		if err != nil {
			switch {
			case errors.Is(err, alarm.ErrNotFound):
				writeRESTError(w, http.StatusNotFound, "ALARM_NOT_FOUND", err.Error())
			default:
				writeRESTError(w, http.StatusBadRequest, "ALARM_COMMENT_FAILED", err.Error())
			}
			return
		}
		writeRESTJSON(w, http.StatusCreated, comment)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
