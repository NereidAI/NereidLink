// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rule_chain_handlers.go exposes chain groups, version listing, graph
// lookup, draft creation and activation for the React Flow editor and the
// binding selectors on profiles and devices.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/rule"
)

// RuleChainAdmin is the rule chain management surface.
type RuleChainAdmin interface {
	ListGroups(ctx context.Context, tenantID string) ([]rule.GroupMeta, error)
	CreateGroup(ctx context.Context, tenantID, name string) (string, error)
	DeleteGroup(ctx context.Context, tenantID, groupID string) error
	ListChainVersions(ctx context.Context, tenantID, groupID string) ([]rule.ChainMeta, error)
	Graph(ctx context.Context, chainID string) (rule.GraphDTO, error)
	CreateDraft(ctx context.Context, tenantID, groupID string, dto rule.GraphDTO) (string, rule.GraphDTO, error)
	Activate(ctx context.Context, tenantID, chainID string) error
	SetGroupEnabled(ctx context.Context, tenantID, groupID string, enabled bool) error
}

// createGroupBody is the new chain group request.
type createGroupBody struct {
	// Name is the tenant-unique chain group name.
	Name string `json:"name"`
}

// draftResponse is the create-draft result, echoing resolved identifiers
// and canvas positions so the editor stays in sync.
type draftResponse struct {
	// ChainID is the new draft version.
	ChainID string `json:"chain_id"`

	// Graph is the stored graph with generated identifiers.
	Graph rule.GraphDTO `json:"graph"`
}

// SetRuleChainAdmin wires the rule chain management service.
func (h *Hub) SetRuleChainAdmin(admin RuleChainAdmin) {
	h.ruleChainAdmin = admin
}

// handleRuleChainGroups lists chain groups or creates one.
func (h *Hub) handleRuleChainGroups(w http.ResponseWriter, r *http.Request) {
	if !h.ruleChainReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		groups, err := h.ruleChainAdmin.ListGroups(r.Context(), tenant)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, groups)
	case http.MethodPost:
		var body createGroupBody
		if !decodeBody(w, r, &body) {
			return
		}
		groupID, err := h.ruleChainAdmin.CreateGroup(r.Context(), tenant, body.Name)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, map[string]string{"id": groupID})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleRuleChainGroup deletes one chain group or toggles its enabled flag.
func (h *Hub) handleRuleChainGroup(w http.ResponseWriter, r *http.Request) {
	if !h.ruleChainReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	group := r.PathValue("group")

	switch r.Method {
	case http.MethodDelete:
		err := h.ruleChainAdmin.DeleteGroup(r.Context(), tenant, group)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPut:
		var body debugToggleBody
		if !decodeBody(w, r, &body) {
			return
		}
		err := h.ruleChainAdmin.SetGroupEnabled(r.Context(), tenant, group, body.Enabled)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleRuleChainVersions lists group versions or stores a new draft.
func (h *Hub) handleRuleChainVersions(w http.ResponseWriter, r *http.Request) {
	if !h.ruleChainReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	group := r.PathValue("group")
	switch r.Method {
	case http.MethodGet:
		chains, err := h.ruleChainAdmin.ListChainVersions(r.Context(), tenant, group)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, chains)
	case http.MethodPost:
		var graph rule.GraphDTO
		if !decodeBody(w, r, &graph) {
			return
		}
		chainID, stored, err := h.ruleChainAdmin.CreateDraft(r.Context(), tenant, group, graph)
		if err != nil {
			writeRuleChainError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, draftResponse{
			ChainID: chainID,
			Graph:   stored,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleRuleChain reads one chain version graph.
func (h *Hub) handleRuleChain(w http.ResponseWriter, r *http.Request) {
	if !h.ruleChainReady(w) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	graph, err := h.ruleChainAdmin.Graph(r.Context(), r.PathValue("chain"))
	if err != nil {
		writeRuleChainError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, graph)
}

// handleActivateChain activates a draft version.
func (h *Hub) handleActivateChain(w http.ResponseWriter, r *http.Request) {
	if !h.ruleChainReady(w) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	err := h.ruleChainAdmin.Activate(r.Context(),
		r.PathValue("tenant"), r.PathValue("chain"))
	if err != nil {
		writeRuleChainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ruleChainReady verifies the admin service is wired.
func (h *Hub) ruleChainReady(w http.ResponseWriter) bool {
	if h.ruleChainAdmin != nil {
		return true
	}
	writeRESTError(w, http.StatusServiceUnavailable, "RULE_CHAIN_SERVICE_UNAVAILABLE",
		"rule chain management service is not enabled")
	return false
}

// writeRuleChainError maps chain domain errors to HTTP status codes.
func writeRuleChainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rule.ErrNoActiveChain):
		writeRESTError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, rule.ErrGroupNotFound), errors.Is(err, rule.ErrChainNotFound):
		writeRESTError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, rule.ErrGroupNameConflict):
		writeRESTError(w, http.StatusConflict, "RULE_CHAIN_NAME_CONFLICT", err.Error())
	case errors.Is(err, rule.ErrGroupInUse):
		writeRESTError(w, http.StatusConflict, "RULE_CHAIN_GROUP_IN_USE", err.Error())
	case errors.Is(err, rule.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "RULE_CHAIN_INVALID_ARGUMENT", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "RULE_CHAIN_OPERATION_FAILED",
			err.Error())
	}
}
