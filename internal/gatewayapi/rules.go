// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rules.go exposes rule-chain debug and test-delivery endpoints defined in
// ADR-0028.

package gatewayapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nereidai/nereidlink/internal/rule"
)

// RuleTools is the rule debug surface consumed by the hub.
type RuleTools interface {
	SetNodeDebug(nodeID string, enabled bool)
	NodeCaptures(nodeID string) json.RawMessage
	SetChainTrace(ctx context.Context, tenantID, chainID string, enabled bool)
	ChainTraces(chainID, traceID string) json.RawMessage
	DryRun(ctx context.Context, tenantID string, data, metadata map[string]any) (json.RawMessage, error)
	DryRunGraph(ctx context.Context, graph rule.GraphDTO, data, metadata map[string]any) (json.RawMessage, error)
}

// debugToggleBody is the node/chain debug toggle document.
type debugToggleBody struct {
	Enabled bool `json:"enabled"`
}

// dryRunBody is the single-chain test-delivery document.
type dryRunBody struct {
	Data     map[string]any `json:"data"`
	Metadata map[string]any `json:"metadata"`
	Graph    *rule.GraphDTO `json:"graph,omitempty"`
}

// handleNodeDebug toggles per-node capture.
func (h *Hub) handleNodeDebug(w http.ResponseWriter, r *http.Request) {
	if h.ruleTools == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RULE_SERVICE_UNAVAILABLE",
			"rule service is not enabled")
		return
	}

	if r.Method == http.MethodGet {
		writeRESTJSON(w, http.StatusOK, h.ruleTools.NodeCaptures(r.PathValue("node")))
		return
	}

	var body debugToggleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	h.ruleTools.SetNodeDebug(r.PathValue("node"), body.Enabled)
	writeRESTJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// handleDryRun executes one test message without real side effects.
func (h *Hub) handleDryRun(w http.ResponseWriter, r *http.Request) {
	if h.ruleTools == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RULE_SERVICE_UNAVAILABLE",
			"rule service is not enabled")
		return
	}

	var body dryRunBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	var trace json.RawMessage
	var err error
	if body.Graph != nil {
		trace, err = h.ruleTools.DryRunGraph(r.Context(),
			*body.Graph, body.Data, body.Metadata)
	} else {
		trace, err = h.ruleTools.DryRun(r.Context(),
			r.PathValue("tenant"), body.Data, body.Metadata)
	}
	if err != nil {
		writeRESTJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"code":    "RULE_DRY_RUN_FAILED",
				"message": err.Error(),
			},
		})
		return
	}
	writeRESTJSON(w, http.StatusOK, trace)
}

// handleChainTrace toggles or returns full-chain traces.
func (h *Hub) handleChainTrace(w http.ResponseWriter, r *http.Request) {
	if h.ruleTools == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "RULE_SERVICE_UNAVAILABLE",
			"rule service is not enabled")
		return
	}

	query := r.URL.Query()
	if r.Method == http.MethodGet {
		writeRESTJSON(w, http.StatusOK,
			h.ruleTools.ChainTraces(query.Get("chain_id"), query.Get("trace_id")))
		return
	}

	var body debugToggleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	chainID := query.Get("chain_id")
	h.ruleTools.SetChainTrace(r.Context(), r.PathValue("tenant"),
		chainID, body.Enabled)
	writeRESTJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}
