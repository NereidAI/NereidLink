// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// forwarder_handlers.go exposes tenant-scoped third-party data forwarding
// (ADR-0018): CRUD over forwarders and a read/replay surface over the
// forwarder dead-letter queue. Delivery itself is asynchronous on the
// bounded JetStream streams managed by internal/egress.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/nereidai/nereidlink/internal/egress"
)

// ForwarderAdmin is the forwarder management surface.
type ForwarderAdmin interface {
	List(ctx context.Context, tenantID string) ([]egress.Forwarder, error)
	Get(ctx context.Context, tenantID, id string) (egress.Forwarder, error)
	Create(ctx context.Context, tenantID, actor string, input egress.Input) (egress.Forwarder, error)
	Update(ctx context.Context, tenantID, id string, input egress.Input) (egress.Forwarder, error)
	Delete(ctx context.Context, tenantID, id string) error
	ListDLQ(ctx context.Context, tenantID, forwarderID string) ([]egress.DeadLetter, error)
	ListDeliveries(ctx context.Context, tenantID, forwarderID string, limit int) ([]egress.Delivery, error)
	Replay(ctx context.Context, tenantID, dlqID string) error
}

// SetForwarderAdmin wires the forwarder management service.
func (h *Hub) SetForwarderAdmin(admin ForwarderAdmin) {
	h.forwarderAdmin = admin
}

// requireForwarderAdmin returns the service or writes a 503.
func (h *Hub) requireForwarderAdmin(w http.ResponseWriter) ForwarderAdmin {
	if h.forwarderAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "FORWARDER_SERVICE_UNAVAILABLE",
			"forwarder service is not enabled")
		return nil
	}
	return h.forwarderAdmin
}

// forwarderBody is the create/update request. Enabled is a pointer so a
// create without it defaults to enabled and an update distinguishes unset
// from false.
type forwarderBody struct {
	Name              string          `json:"name"`
	SinkType          string          `json:"sink_type"`
	SinkConfig        json.RawMessage `json:"sink_config"`
	Encoder           string          `json:"encoder"`
	MaxAttempts       int             `json:"max_attempts"`
	MessageTTLMS      int64           `json:"message_ttl_ms"`
	MaxBufferMessages int             `json:"max_buffer_messages"`
	Enabled           *bool           `json:"enabled"`
}

// toInput converts the request body to a service input. enabledDefault is
// used when body.Enabled is nil (true for create, true for update).
func (b forwarderBody) toInput(enabledDefault bool) egress.Input {
	enabled := enabledDefault
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	cfg := map[string]any{}
	if len(b.SinkConfig) > 0 {
		_ = json.Unmarshal(b.SinkConfig, &cfg)
	}
	return egress.Input{
		Name:              b.Name,
		SinkType:          b.SinkType,
		SinkConfig:        cfg,
		Encoder:           b.Encoder,
		MaxAttempts:       b.MaxAttempts,
		MessageTTLMS:      b.MessageTTLMS,
		MaxBufferMessages: b.MaxBufferMessages,
		Enabled:           enabled,
	}
}

// handleForwarders dispatches the collection routes.
func (h *Hub) handleForwarders(w http.ResponseWriter, r *http.Request) {
	admin := h.requireForwarderAdmin(w)
	if admin == nil {
		return
	}
	tenant := r.PathValue("tenant")

	if r.Method == http.MethodPost {
		body, ok := decodeForwarderBody(w, r)
		if !ok {
			return
		}
		f, err := admin.Create(r.Context(), tenant, forwarderActor(r),
			body.toInput(true))
		if h.writeForwarderError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusCreated, f)
		return
	}

	list, err := admin.List(r.Context(), tenant)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "FORWARDER_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, list)
}

// handleForwarder dispatches one-forwarder routes.
func (h *Hub) handleForwarder(w http.ResponseWriter, r *http.Request) {
	admin := h.requireForwarderAdmin(w)
	if admin == nil {
		return
	}
	tenant := r.PathValue("tenant")
	id := r.PathValue("forwarder")

	switch r.Method {
	case http.MethodGet:
		f, err := admin.Get(r.Context(), tenant, id)
		if h.writeForwarderError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusOK, f)

	case http.MethodPut:
		body, ok := decodeForwarderBody(w, r)
		if !ok {
			return
		}
		f, err := admin.Update(r.Context(), tenant, id, body.toInput(true))
		if h.writeForwarderError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusOK, f)

	case http.MethodDelete:
		if err := admin.Delete(r.Context(), tenant, id); h.writeForwarderError(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleForwarderDLQ lists dead letters, optionally for one forwarder.
func (h *Hub) handleForwarderDLQ(w http.ResponseWriter, r *http.Request) {
	admin := h.requireForwarderAdmin(w)
	if admin == nil {
		return
	}
	list, err := admin.ListDLQ(r.Context(), r.PathValue("tenant"),
		r.URL.Query().Get("forwarder"))
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "FORWARDER_DLQ_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, list)
}

// handleForwarderDeliveries lists delivery-log rows, optionally for one
// forwarder. The limit query parameter bounds the response.
func (h *Hub) handleForwarderDeliveries(w http.ResponseWriter, r *http.Request) {
	admin := h.requireForwarderAdmin(w)
	if admin == nil {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	list, err := admin.ListDeliveries(r.Context(), r.PathValue("tenant"),
		r.URL.Query().Get("forwarder"), limit)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "FORWARDER_DELIVERY_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, list)
}

// handleForwarderReplay re-publishes one dead-letter envelope.
func (h *Hub) handleForwarderReplay(w http.ResponseWriter, r *http.Request) {
	admin := h.requireForwarderAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.Replay(r.Context(), r.PathValue("tenant"),
		r.PathValue("dlq")); h.writeForwarderError(w, err) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// forwarderActor returns the principal id for the created_by audit column.
// The column is a UUID, so the user id (not the email) is required.
func forwarderActor(r *http.Request) string {
	if p := contextPrincipal(r.Context()); p != nil && p.UserID != "" {
		return p.UserID
	}
	return ""
}

// decodeForwarderBody decodes a create/update body.
func decodeForwarderBody(w http.ResponseWriter, r *http.Request) (forwarderBody, bool) {
	var body forwarderBody
	if !decodeBody(w, r, &body) {
		return forwarderBody{}, false
	}
	return body, true
}

// writeForwarderError maps egress errors onto REST errors.
func (h *Hub) writeForwarderError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, egress.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "FORWARDER_NOT_FOUND", err.Error())
	case errors.Is(err, egress.ErrNameTaken):
		writeRESTError(w, http.StatusConflict, "FORWARDER_NAME_TAKEN", err.Error())
	case errors.Is(err, egress.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "FORWARDER_INVALID_ARGUMENT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "FORWARDER_OPERATION_FAILED", err.Error())
	}
	return true
}
