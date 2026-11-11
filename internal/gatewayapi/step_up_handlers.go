// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// step_up_handlers.go exposes the operation-time step-up flow over tenant
// REST (ADR-0035): start a challenge for a registered action, complete it
// with a current TOTP code, and (in this ticket) exercise the guard through
// a minimal demo endpoint. Later tickets register their dangerous actions in
// stepUpActionPoints and call stepUp.Consume directly.
//
// Route-level RBAC only proves tenant access (stats:read); the action's real
// permission point and the human-only rule are enforced here so the mapping
// action -> point cannot be influenced by request content.

package gatewayapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/stepup"
)

// stepUpActionPoints maps registered action keys to the RBAC point the
// caller must already hold. Only keys present here can be challenged. Later
// feature tickets add edge_operation:exec, config bundle actions, etc.
var stepUpActionPoints = map[string]string{
	// Demo action proving the end-to-end guard without business UI.
	"step_up:demo": rbac.PointStats,
	// Forced vault deletion of a secret still delivered to gateways
	// (ticket 07).
	"edge_secret:force-delete": rbac.PointEdgeSecretWrite,
}

// SetStepUp wires the step-up service.
func (h *Hub) SetStepUp(service *stepup.Service) {
	h.stepUp = service
}

// stepUpStartBody starts a challenge for action with non-sensitive details.
type stepUpStartBody struct {
	Action  string         `json:"action"`
	Details map[string]any `json:"details,omitempty"`
}

// stepUpCompleteBody carries the current TOTP code.
type stepUpCompleteBody struct {
	Code string `json:"code"`
}

// stepUpConsumeBody identifies the consumed challenge from a protected route.
type stepUpConsumeBody struct {
	StepUpID string `json:"step_up_id"`
}

// handleStepUpChallenges starts a challenge.
func (h *Hub) handleStepUpChallenges(w http.ResponseWriter, r *http.Request) {
	if h.stepUp == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
			"step-up is not enabled")
		return
	}
	principal := contextPrincipal(r.Context())
	if principal == nil || principal.Machine {
		writeRESTError(w, http.StatusForbidden, "STEP_UP_HUMAN_ONLY",
			"step-up requires a human user")
		return
	}

	var body stepUpStartBody
	if !decodeBody(w, r, &body) {
		return
	}
	tenant := r.PathValue("tenant")
	// Per-operation dangerous actions (ADR-0035) use a registered prefix so
	// a challenge is bound to one operation key; every such action requires the
	// exec point.
	if operation, isEdgeOp := strings.CutPrefix(body.Action, stepUpActionEdgeInvoke); isEdgeOp {
		if operation == "" {
			writeRESTError(w, http.StatusBadRequest, "STEP_UP_UNKNOWN_ACTION",
				"unknown step-up action")
			return
		}
		if !principal.HasPoint(tenant, rbac.PointEdgeOperationExec) {
			writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
				"missing permission point: "+rbac.PointEdgeOperationExec)
			return
		}
	} else {
		point, ok := stepUpActionPoints[body.Action]
		if !ok {
			writeRESTError(w, http.StatusBadRequest, "STEP_UP_UNKNOWN_ACTION",
				"unknown step-up action")
			return
		}
		if !principal.HasPoint(tenant, point) {
			writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
				"missing permission point: "+point)
			return
		}
	}

	challenge, err := h.stepUp.Start(r.Context(), stepup.StartInput{
		TenantID: tenant,
		UserID:   principal.UserID,
		Action:   body.Action,
		Details:  body.Details,
	})
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "STEP_UP_START_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusCreated, challenge)
}

// handleStepUpChallenge completes a pending challenge with a TOTP code.
func (h *Hub) handleStepUpChallenge(w http.ResponseWriter, r *http.Request) {
	if h.stepUp == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
			"step-up is not enabled")
		return
	}
	principal := contextPrincipal(r.Context())
	if principal == nil || principal.Machine {
		writeRESTError(w, http.StatusForbidden, "STEP_UP_HUMAN_ONLY",
			"step-up requires a human user")
		return
	}
	var body stepUpCompleteBody
	if !decodeBody(w, r, &body) {
		return
	}
	tenant := r.PathValue("tenant")
	challenge, err := h.stepUp.Complete(r.Context(), tenant,
		principal.UserID, r.PathValue("challenge"), body.Code, requestIP(r))
	if err != nil {
		writeStepUpError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, challenge)
}

// handleStepUpDemo is the minimal protected endpoint proving the guard
// end-to-end: it consumes the challenge for step_up:demo.
func (h *Hub) handleStepUpDemo(w http.ResponseWriter, r *http.Request) {
	if h.stepUp == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
			"step-up is not enabled")
		return
	}
	principal := contextPrincipal(r.Context())
	if principal == nil || principal.Machine {
		writeRESTError(w, http.StatusForbidden, "STEP_UP_HUMAN_ONLY",
			"step-up requires a human user")
		return
	}
	var body stepUpConsumeBody
	if !decodeBody(w, r, &body) {
		return
	}
	const action = "step_up:demo"
	if err := h.stepUp.Consume(r.Context(), r.PathValue("tenant"),
		principal.UserID, body.StepUpID, action, requestIP(r)); err != nil {
		writeStepUpError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// writeStepUpError maps step-up/identity errors to the REST error envelope.
func writeStepUpError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrTOTPNotEnrolled):
		writeRESTError(w, http.StatusPreconditionRequired, "STEP_UP_TOTP_NOT_ENROLLED",
			"enroll an authenticator app before performing this action")
	case errors.Is(err, identity.ErrInvalidTOTP):
		writeRESTError(w, http.StatusUnauthorized, "STEP_UP_INVALID_CODE",
			"the TOTP code was incorrect")
	case errors.Is(err, identity.ErrUserLocked):
		writeRESTError(w, http.StatusForbidden, "STEP_UP_LOCKED",
			"the account is temporarily locked after failed attempts")
	case errors.Is(err, identity.ErrUserDisabled):
		writeRESTError(w, http.StatusForbidden, "STEP_UP_USER_DISABLED",
			"the account is disabled")
	case errors.Is(err, stepup.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "STEP_UP_NOT_FOUND",
			"step-up challenge not found")
	case errors.Is(err, stepup.ErrExpired):
		writeRESTError(w, http.StatusGone, "STEP_UP_EXPIRED",
			"the step-up challenge expired; start a new one")
	case errors.Is(err, stepup.ErrReplay):
		writeRESTError(w, http.StatusConflict, "STEP_UP_REPLAY",
			"this step-up challenge was already used")
	case errors.Is(err, stepup.ErrActionMismatch):
		writeRESTError(w, http.StatusBadRequest, "STEP_UP_ACTION_MISMATCH",
			"the challenge was not issued for this action")
	case errors.Is(err, stepup.ErrNotPending), errors.Is(err, stepup.ErrNotVerified):
		writeRESTError(w, http.StatusConflict, "STEP_UP_INVALID_STATE",
			"the step-up challenge is not in the required state")
	default:
		writeRESTError(w, http.StatusInternalServerError, "STEP_UP_FAILED", err.Error())
	}
}
