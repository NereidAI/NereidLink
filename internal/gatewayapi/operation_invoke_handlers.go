// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// operation_invoke_handlers.go exposes immediate whole-gateway operation
// execution and task history over tenant REST (ADR-0035). Risk enforcement
// beyond RBAC lives here: dangerous operations require a human principal and
// a fresh consumed TOTP step-up; the risk level comes exclusively from the
// server-side catalog, never from request content.

package gatewayapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/opinvoke"
)

// OperationInvoker executes invocations.
type OperationInvoker interface {
	Invoke(ctx context.Context, in opinvoke.InvokeInput) (opinvoke.Task, error)
	Tasks(ctx context.Context, tenantID, gatewayID string, limit int) ([]opinvoke.Task, error)
	GetTask(ctx context.Context, tenantID, id string) (opinvoke.Task, error)
}

// SetOperationInvoker wires the invocation service.
func (h *Hub) SetOperationInvoker(service OperationInvoker) {
	h.operationTasks = service
}

// operationInvokeBody carries params (secret:// references allowed), an
// optional per-call timeout (may only shorten the descriptor default) and,
// for a dangerous operation, the consumed step-up challenge id.
type operationInvokeBody struct {
	Params    map[string]any `json:"params,omitempty"`
	TimeoutMs int            `json:"timeout_ms,omitempty"`
	StepUpID  string         `json:"step_up_id,omitempty"`
}

// stepUpActionFor returns the challenge action key bound to one operation.
// The suffix binds the verified challenge to this operation key.
func stepUpActionFor(operation string) string {
	return stepUpActionEdgeInvoke + operation
}

const stepUpActionEdgeInvoke = "edge_operation:invoke:"

// handleOperationInvoke executes one catalog operation.
func (h *Hub) handleOperationInvoke(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	gateway := r.PathValue("gateway")
	operation := r.PathValue("operation")

	var body operationInvokeBody
	if !decodeBody(w, r, &body) {
		return
	}

	risk, ok := h.operationRisk(r, tenant, gateway, operation)
	if !ok {
		writeRESTError(w, http.StatusNotFound, "OPERATION_NOT_FOUND",
			"gateway operation not found or disabled")
		return
	}

	principal := contextPrincipal(r.Context())
	// Gateway operations are human-only at every risk level: a machine
	// identity cannot satisfy the dangerous-action step-up, and the exec
	// points are not machine-grantable. This is the handler-side half of
	// that invariant (the scope CHECK is the data-layer half).
	if principal == nil || principal.Machine {
		if risk == opcatalog.RiskDangerous {
			h.auditOperationDenied(r, gateway, operation, risk, "machine_identity")
		}
		writeRESTError(w, http.StatusForbidden, "OPERATION_HUMAN_ONLY",
			"gateway operations require a human operator")
		return
	}
	if risk == opcatalog.RiskDangerous {
		// Dangerous actions additionally require a fresh one-time step-up
		// consumed within its 5-minute window.
		if body.StepUpID == "" {
			h.auditOperationDenied(r, gateway, operation, risk, "step_up_required")
			writeRESTError(w, http.StatusPreconditionRequired, "STEP_UP_REQUIRED",
				"complete the authenticator code before this operation")
			return
		}
		if h.stepUp == nil {
			writeRESTError(w, http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
				"step-up is not enabled")
			return
		}
		if err := h.stepUp.Consume(r.Context(), tenant, principal.UserID,
			body.StepUpID, stepUpActionFor(operation), requestIP(r)); err != nil {
			h.auditOperationDenied(r, gateway, operation, risk, "step_up_invalid")
			writeStepUpError(w, err)
			return
		}
	}

	if h.operationTasks == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "OPERATION_INVOKE_UNAVAILABLE",
			"operation invocation is not enabled")
		return
	}
	task, err := h.operationTasks.Invoke(r.Context(), opinvoke.InvokeInput{
		TenantID:  tenant,
		GatewayID: gateway,
		Operation: operation,
		Params:    body.Params,
		TimeoutMs: body.TimeoutMs,
		Actor: opinvoke.Actor{
			UserID: principal.UserID,
			Email:  principal.Email,
		},
	})
	if err != nil {
		writeOpInvokeError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// auditOperationDenied records a dangerous attempt rejected before any task
// is created (machine principal, missing or invalid step-up). The chained
// log must capture refused attempts, not only executed ones.
func (h *Hub) auditOperationDenied(r *http.Request, gateway, operation, risk, reason string) {
	if h.auditReader == nil {
		return
	}
	event := audit.Input{
		TenantID:     r.PathValue("tenant"),
		EventType:    audit.EventGatewayOperation,
		Action:       "invoke_denied",
		ResourceType: "gateway",
		ResourceID:   gateway,
		IP:           requestIP(r),
		Details: map[string]any{
			"operation": operation,
			"risk":      risk,
			"reason":    reason,
			"status":    "DENIED",
		},
	}
	if p := contextPrincipal(r.Context()); p != nil {
		event.ActorID = p.UserID
		event.ActorName = p.Email
	}
	if _, err := h.auditReader.Append(r.Context(), event); err != nil {
		slog.Error("failed to audit denied gateway operation",
			"operation", operation, "error", err)
	}
}

// operationRisk resolves the risk of the descriptor the target gateway itself
// advertises (or its tenant-private fallback). Disabled or missing operations
// return ok=false. This is enforced per gateway so same-key contracts on
// different gateways cannot bleed into one another.
func (h *Hub) operationRisk(r *http.Request, tenant, gateway, operation string) (string, bool) {
	if h.operations == nil {
		return "", false
	}
	view, ok, err := h.operations.EffectiveForGateway(r.Context(), tenant, gateway, operation)
	if err != nil || !ok || view.Disabled {
		return "", false
	}
	return view.Risk, true
}

// handleOperationTasks returns the task history for one gateway.
func (h *Hub) handleOperationTasks(w http.ResponseWriter, r *http.Request) {
	limit := DefaultTaskHistory
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	tasks, err := h.operationTasks.Tasks(r.Context(),
		r.PathValue("tenant"), r.PathValue("gateway"), limit)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "OPERATION_TASKS_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, tasks)
}

// handleOperationTask returns one task.
func (h *Hub) handleOperationTask(w http.ResponseWriter, r *http.Request) {
	task, err := h.operationTasks.GetTask(r.Context(),
		r.PathValue("tenant"), r.PathValue("task"))
	if err != nil {
		writeOpInvokeError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, task)
}

// DefaultTaskHistory bounds one history query when limit is omitted.
const DefaultTaskHistory = 50

// writeOpInvokeError maps invocation errors to the REST error envelope.
func writeOpInvokeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, opinvoke.ErrOperationNotFound):
		writeRESTError(w, http.StatusNotFound, "OPERATION_NOT_FOUND",
			"gateway operation not found")
	case errors.Is(err, opinvoke.ErrOperationDisabled):
		writeRESTError(w, http.StatusConflict, "OPERATION_DISABLED",
			"gateway operation is disabled")
	case errors.Is(err, opinvoke.ErrParamsTooLarge):
		writeRESTError(w, http.StatusRequestEntityTooLarge, "OPERATION_PARAMS_TOO_LARGE",
			"operation params exceed 64KB")
	case errors.Is(err, opinvoke.ErrParamsSchema):
		writeRESTError(w, http.StatusBadRequest, "OPERATION_PARAMS_INVALID",
			err.Error())
	case errors.Is(err, opinvoke.ErrResultTooLarge):
		writeRESTError(w, http.StatusBadGateway, "OPERATION_RESULT_TOO_LARGE",
			"operation result exceeds 64KB")
	case errors.Is(err, opinvoke.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "OPERATION_TASK_NOT_FOUND",
			"operation task not found")
	default:
		writeRESTError(w, http.StatusInternalServerError, "OPERATION_INVOKE_FAILED", err.Error())
	}
}
