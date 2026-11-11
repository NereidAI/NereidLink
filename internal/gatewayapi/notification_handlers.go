// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// notification_handlers.go exposes inbox listing and read-state updates.
// The notification frames themselves are pushed over WebSocket from the
// nl.notification subjects.

package gatewayapi

import (
	"context"
	"net/http"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/notify"
)

// NotificationInbox is the notification query/update surface.
type NotificationInbox interface {
	List(ctx context.Context, tenantID, state string, page, pageSize int) (apipage.Page[notify.Notification], error)
	MarkRead(ctx context.Context, tenantID string, ids []string) error
}

// markReadBody carries the identifiers to mark as read.
type markReadBody struct {
	// IDs are the notification identifiers.
	IDs []string `json:"ids"`
}

// SetNotificationInbox wires the notification inbox source.
func (h *Hub) SetNotificationInbox(inbox NotificationInbox) {
	h.notificationInbox = inbox
}

// handleListNotifications returns a page of notifications, optionally
// filtered by the state query parameter.
func (h *Hub) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	if h.notificationInbox == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "NOTIFICATION_SERVICE_UNAVAILABLE",
			"notification inbox is not enabled")
		return
	}

	page, pageSize := parsePagination(r)
	result, err := h.notificationInbox.List(r.Context(),
		r.PathValue("tenant"), r.URL.Query().Get("state"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "NOTIFICATION_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleMarkNotificationsRead marks the supplied notifications as read.
func (h *Hub) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if h.notificationInbox == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "NOTIFICATION_SERVICE_UNAVAILABLE",
			"notification inbox is not enabled")
		return
	}

	var body markReadBody
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.notificationInbox.MarkRead(r.Context(),
		r.PathValue("tenant"), body.IDs); err != nil {
		writeRESTError(w, http.StatusInternalServerError, "NOTIFICATION_MARK_READ_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
