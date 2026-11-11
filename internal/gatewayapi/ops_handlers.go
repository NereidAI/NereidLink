// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ops_handlers.go exposes the ADR-0023 operation order workflow split
// across two listeners:
//
//   - The standalone ops admin listener (loopback) serves the operator
//     CLI: create destructive orders, inspect them and withdraw them.
//     It needs no application login because it binds loopback and the
//     operator identity (OS user) is recorded on the order.
//   - The business API serves the single super administrator on the
//     web: list orders and confirm (approve/reject). Confirmation is
//     what authorizes execution.
//
// Orders cannot be created on the web and cannot be confirmed on the
// CLI; the channel stamped at registration enforces that split.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/opscenter"
)

// OpsCenter is the operation order surface. *opscenter.Service
// satisfies it.
type OpsCenter interface {
	Create(ctx context.Context, input opscenter.CreateOrderInput) (opscenter.Order, error)
	Get(ctx context.Context, id string) (opscenter.Order, error)
	List(ctx context.Context, filter opscenter.ListFilter) ([]opscenter.Order, error)
	Approve(ctx context.Context, id string, reviewer opscenter.Reviewer) (opscenter.Order, error)
	Reject(ctx context.Context, id string, reviewer opscenter.Reviewer) (opscenter.Order, error)
	Cancel(ctx context.Context, id, operatorName string) (opscenter.Order, error)
}

// SetOpsCenter wires the operation order service.
func (h *Hub) SetOpsCenter(center OpsCenter) {
	h.opsCenter = center
}

// createOpsOrderBody carries an operator CLI request. Payload is a
// JSON object validated by the action executor; an absent payload
// becomes {}.
type createOpsOrderBody struct {
	TenantID   string          `json:"tenant_id"`
	Action     string          `json:"action"`
	Summary    string          `json:"summary"`
	Payload    json.RawMessage `json:"payload"`
	Operator   string          `json:"operator"`
	TTLSeconds int64           `json:"ttl_seconds"`
}

// cancelBody identifies the withdrawing operator.
type cancelBody struct {
	Operator string `json:"operator"`
}

// reviewBody carries the optional decision rationale.
type reviewBody struct {
	Reason string `json:"reason"`
}

// DeadmanProbe exposes the platform dead-man fallback channel
// (ADR-0019) so an operator can verify the independent endpoint from the
// loopback ops listener without waiting for a real outage. It is
// deliberately an interface so the hub does not depend on the delivery
// implementation.
type DeadmanProbe interface {
	// Report delivers one platform problem through the independent
	// fallback channel and returns the first delivery error, if any.
	Report(ctx context.Context, code, subject, body string) error
}

// SetDeadmanProbe wires the dead-man fallback channel.
func (h *Hub) SetDeadmanProbe(probe DeadmanProbe) {
	h.deadmanProbe = probe
}

// RegisterOpsInitiator adds the CLI-side routes (create, inspect,
// cancel) to the standalone ops admin mux.
func (h *Hub) RegisterOpsInitiator(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ops/orders", h.handleOpsCreate)
	mux.HandleFunc("GET /v1/ops/orders", h.handleOpsList)
	mux.HandleFunc("GET /v1/ops/orders/{id}", h.handleOpsGet)
	mux.HandleFunc("POST /v1/ops/orders/{id}/cancel", h.handleOpsCancel)
	mux.HandleFunc("POST /v1/ops/deadman/test", h.handleOpsDeadmanTest)
}

// RegisterOpsReview adds the web-side routes (list/get, confirm) to
// the business API mux. They sit behind the platform-admin RBAC guard.
func (h *Hub) RegisterOpsReview(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ops/orders", h.handleOpsList)
	mux.HandleFunc("GET /v1/ops/orders/{id}", h.handleOpsGet)
	mux.HandleFunc("POST /v1/ops/orders/{id}/approve", h.handleOpsApprove)
	mux.HandleFunc("POST /v1/ops/orders/{id}/reject", h.handleOpsReject)
}

// OpsServeMux builds the standalone ops admin listener mux (CLI
// channel) with no business routes.
func (h *Hub) OpsServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h.RegisterOpsInitiator(mux)
	return mux
}

// handleOpsDeadmanTest sends one synthetic report through the independent
// dead-man fallback channel so an operator can confirm the endpoint is
// reachable. It never touches tenant notification state.
func (h *Hub) handleOpsDeadmanTest(w http.ResponseWriter, r *http.Request) {
	if h.deadmanProbe == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DEADMAN_UNAVAILABLE",
			"dead-man fallback channel is not configured")
		return
	}
	var body struct {
		Code    string `json:"code"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if r.ContentLength > 0 && !decodeBody(w, r, &body) {
		return
	}
	if body.Code == "" {
		body.Code = "PLATFORM_PROBLEM_TEST"
	}
	if body.Subject == "" {
		body.Subject = "NereidLink dead-man test"
	}
	if body.Body == "" {
		body.Body = "This is an operator-triggered dead-man delivery test."
	}
	if err := h.deadmanProbe.Report(r.Context(), body.Code, body.Subject, body.Body); err != nil {
		writeRESTError(w, http.StatusBadGateway, "DEADMAN_DELIVERY_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
}

// requireOpsCenter verifies the service is wired.
func (h *Hub) requireOpsCenter(w http.ResponseWriter) (OpsCenter, bool) {
	if h.opsCenter == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPS_SERVICE_UNAVAILABLE",
			"operation orders are not enabled")
		return nil, false
	}
	return h.opsCenter, true
}

// handleOpsCreate opens an order on behalf of the CLI operator,
// stamping channel CLI.
func (h *Hub) handleOpsCreate(w http.ResponseWriter, r *http.Request) {
	center, ok := h.requireOpsCenter(w)
	if !ok {
		return
	}
	var body createOpsOrderBody
	if !decodeBody(w, r, &body) {
		return
	}
	var ttl time.Duration
	if body.TTLSeconds > 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	order, err := center.Create(r.Context(), opscenter.CreateOrderInput{
		TenantID:         body.TenantID,
		Action:           body.Action,
		Summary:          body.Summary,
		Payload:          body.Payload,
		InitiatorName:    body.Operator,
		InitiatorChannel: opscenter.ChannelCLI,
		TTL:              ttl,
	})
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, order)
}

// handleOpsList returns orders filtered by status and/or tenant.
func (h *Hub) handleOpsList(w http.ResponseWriter, r *http.Request) {
	center, ok := h.requireOpsCenter(w)
	if !ok {
		return
	}
	orders, err := center.List(r.Context(), opscenter.ListFilter{
		Status:   r.URL.Query().Get("status"),
		TenantID: r.URL.Query().Get("tenant_id"),
	})
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, orders)
}

// handleOpsGet returns one order.
func (h *Hub) handleOpsGet(w http.ResponseWriter, r *http.Request) {
	center, ok := h.requireOpsCenter(w)
	if !ok {
		return
	}
	order, err := center.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, order)
}

// handleOpsApprove confirms an order as the authenticated super
// administrator, stamping channel WEB.
func (h *Hub) handleOpsApprove(w http.ResponseWriter, r *http.Request) {
	h.review(w, r, true)
}

// handleOpsReject refuses an order as the authenticated super
// administrator.
func (h *Hub) handleOpsReject(w http.ResponseWriter, r *http.Request) {
	h.review(w, r, false)
}

// review performs the shared approve/reject flow using the web
// principal as the reviewer.
func (h *Hub) review(w http.ResponseWriter, r *http.Request, approve bool) {
	center, ok := h.requireOpsCenter(w)
	if !ok {
		return
	}
	var body reviewBody
	if !decodeBody(w, r, &body) {
		return
	}
	principal := contextPrincipal(r.Context())
	reviewer := opscenter.Reviewer{
		ID:      principal.UserID,
		Name:    principal.Email,
		Channel: opscenter.ChannelWeb,
		Reason:  body.Reason,
	}
	var order opscenter.Order
	var err error
	if approve {
		order, err = center.Approve(r.Context(), r.PathValue("id"), reviewer)
	} else {
		order, err = center.Reject(r.Context(), r.PathValue("id"), reviewer)
	}
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, order)
}

// handleOpsCancel withdraws an order; the operator name in the body
// must match the order's initiator.
func (h *Hub) handleOpsCancel(w http.ResponseWriter, r *http.Request) {
	center, ok := h.requireOpsCenter(w)
	if !ok {
		return
	}
	var body cancelBody
	if !decodeBody(w, r, &body) {
		return
	}
	order, err := center.Cancel(r.Context(), r.PathValue("id"), body.Operator)
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, order)
}

// writeOpsError maps opscenter sentinels to HTTP status codes.
func writeOpsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, opscenter.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "OPS_ORDER_NOT_FOUND", err.Error())
	case errors.Is(err, opscenter.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "OPS_ORDER_INVALID_ARGUMENT", err.Error())
	case errors.Is(err, opscenter.ErrNotPending):
		writeRESTError(w, http.StatusConflict, "OPS_ORDER_NOT_PENDING", err.Error())
	case errors.Is(err, opscenter.ErrNotApproved):
		writeRESTError(w, http.StatusConflict, "OPS_ORDER_NOT_APPROVED", err.Error())
	case errors.Is(err, opscenter.ErrOrderExpired):
		writeRESTError(w, http.StatusConflict, "OPS_ORDER_EXPIRED", err.Error())
	case errors.Is(err, opscenter.ErrSameChannel):
		writeRESTError(w, http.StatusForbidden, "OPS_SAME_CHANNEL", err.Error())
	case errors.Is(err, opscenter.ErrNotInitiator):
		writeRESTError(w, http.StatusForbidden, "OPS_NOT_INITIATOR", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "OPS_OPERATION_FAILED", err.Error())
	}
}
