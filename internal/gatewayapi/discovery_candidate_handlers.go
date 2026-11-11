// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discovery_candidate_handlers.go exposes the human-review approval queue
// for gateway auto-provisioning (ADR-0026, migration 0046). Operators list
// pending candidates, read one, and approve (creates the device, pushes the
// result to the gateway) or reject it. Listing/reading needs device:read;
// deciding needs device:write.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/discovery"
)

// DiscoveryApprovalAdmin is the approval-queue management surface.
type DiscoveryApprovalAdmin interface {
	ListCandidates(ctx context.Context, tenantID, state, gatewayID string,
		page, pageSize int) (apipage.Page[discovery.CandidateRecord], error)
	GetCandidate(ctx context.Context, tenantID, candidateID string) (discovery.CandidateRecord, error)
	ApproveCandidate(ctx context.Context, tenantID, candidateID, actor string) (discovery.CandidateRecord, discovery.ItemResult, error)
	RejectCandidate(ctx context.Context, tenantID, candidateID, actor, reason string) (discovery.CandidateRecord, error)
}

// SetDiscoveryApprovalAdmin wires the approval queue.
func (h *Hub) SetDiscoveryApprovalAdmin(admin DiscoveryApprovalAdmin) {
	h.discoveryApprovals = admin
}

// discoveryRejectBody carries an optional operator reason.
type discoveryRejectBody struct {
	Reason string `json:"reason"`
}

// handleDiscoveryCandidates lists a tenant's queue (state/gateway filters).
func (h *Hub) handleDiscoveryCandidates(w http.ResponseWriter, r *http.Request) {
	if h.discoveryApprovals == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_APPROVAL_UNAVAILABLE",
			"discovery approval queue is not enabled")
		return
	}
	q := r.URL.Query()
	page, pageSize := parsePagination(r)
	result, err := h.discoveryApprovals.ListCandidates(r.Context(), r.PathValue("tenant"),
		q.Get("state"), q.Get("gateway"), page, pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "DISCOVERY_CANDIDATE_LIST_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleDiscoveryCandidate reads one candidate.
func (h *Hub) handleDiscoveryCandidate(w http.ResponseWriter, r *http.Request) {
	cand, ok := h.loadDiscoveryCandidate(w, r)
	if !ok {
		return
	}
	writeRESTJSON(w, http.StatusOK, cand)
}

// handleDiscoveryCandidateApprove creates the queued device.
func (h *Hub) handleDiscoveryCandidateApprove(w http.ResponseWriter, r *http.Request) {
	if h.discoveryApprovals == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_APPROVAL_UNAVAILABLE",
			"discovery approval queue is not enabled")
		return
	}
	cand, item, err := h.discoveryApprovals.ApproveCandidate(r.Context(),
		r.PathValue("tenant"), r.PathValue("candidate"), actorFromRequest(r))
	if err != nil {
		writeDiscoveryCandidateError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"candidate": cand, "result": item})
}

// handleDiscoveryCandidateReject declines a candidate.
func (h *Hub) handleDiscoveryCandidateReject(w http.ResponseWriter, r *http.Request) {
	if h.discoveryApprovals == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_APPROVAL_UNAVAILABLE",
			"discovery approval queue is not enabled")
		return
	}
	reason := ""
	if r.ContentLength != 0 {
		var body discoveryRejectBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
			return
		}
		reason = body.Reason
	}
	cand, err := h.discoveryApprovals.RejectCandidate(r.Context(),
		r.PathValue("tenant"), r.PathValue("candidate"), actorFromRequest(r), reason)
	if err != nil {
		writeDiscoveryCandidateError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, cand)
}

// loadDiscoveryCandidate fetches one tenant-scoped candidate or writes the
// error response and reports false.
func (h *Hub) loadDiscoveryCandidate(w http.ResponseWriter, r *http.Request) (discovery.CandidateRecord, bool) {
	if h.discoveryApprovals == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_APPROVAL_UNAVAILABLE",
			"discovery approval queue is not enabled")
		return discovery.CandidateRecord{}, false
	}
	cand, err := h.discoveryApprovals.GetCandidate(r.Context(),
		r.PathValue("tenant"), r.PathValue("candidate"))
	if err != nil {
		writeDiscoveryCandidateError(w, err)
		return discovery.CandidateRecord{}, false
	}
	return cand, true
}

// actorFromRequest returns the deciding principal's user id.
func actorFromRequest(r *http.Request) string {
	if p := contextPrincipal(r.Context()); p != nil && p.UserID != "" {
		return p.UserID
	}
	return "anonymous"
}

func writeDiscoveryCandidateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discovery.ErrCandidateNotFound):
		writeRESTError(w, http.StatusNotFound, "DISCOVERY_CANDIDATE_NOT_FOUND", "candidate not found")
	case errors.Is(err, discovery.ErrCandidateNotPending):
		writeRESTError(w, http.StatusConflict, "DISCOVERY_CANDIDATE_DECIDED", "candidate already has a decision")
	case errors.Is(err, discovery.ErrApprovalRejected):
		writeRESTError(w, http.StatusUnprocessableEntity, "DISCOVERY_CANDIDATE_REJECTED", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "DISCOVERY_CANDIDATE_OPERATION_FAILED", err.Error())
	}
}
