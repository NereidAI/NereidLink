// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discovery_policy_handlers.go exposes per-tenant and per-gateway
// auto-provisioning policy overrides (ADR-0026, migration 0045). GET returns
// the effective policy (gateway -> tenant -> deployment YAML) with an
// `overridden` flag; PUT upserts an override whose omitted fields keep
// inheriting; DELETE removes the override and reverts the scope to
// inheritance. The frame path stays in internal/discovery.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/discovery"
)

// DiscoveryPolicyAdmin is the policy management surface.
type DiscoveryPolicyAdmin interface {
	EffectiveView(ctx context.Context, tenantID, gatewayID string) (discovery.View, error)
	SetTenantPolicy(ctx context.Context, tenantID string, in discovery.Override) (discovery.View, error)
	SetGatewayPolicy(ctx context.Context, tenantID, gatewayID string, in discovery.Override) (discovery.View, error)
	DeleteTenantPolicy(ctx context.Context, tenantID string) error
	DeleteGatewayPolicy(ctx context.Context, tenantID, gatewayID string) error
}

// SetDiscoveryPolicyAdmin wires policy management.
func (h *Hub) SetDiscoveryPolicyAdmin(admin DiscoveryPolicyAdmin) {
	h.discoveryPolicy = admin
}

// handleDiscoveryTenantPolicy serves GET/PUT/DELETE at the tenant scope.
func (h *Hub) handleDiscoveryTenantPolicy(w http.ResponseWriter, r *http.Request) {
	if h.discoveryPolicy == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_POLICY_UNAVAILABLE",
			"discovery policy management is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		view, err := h.discoveryPolicy.EffectiveView(r.Context(), tenant, "")
		if err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, view)
	case http.MethodPut:
		var body discovery.Override
		if !decodeBody(w, r, &body) {
			return
		}
		if overrideEmpty(body) {
			writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_EMPTY",
				"provide at least one of enabled, max_per_cycle, max_candidates, default_profile")
			return
		}
		view, err := h.discoveryPolicy.SetTenantPolicy(r.Context(), tenant, body)
		if err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		if err := h.discoveryPolicy.DeleteTenantPolicy(r.Context(), tenant); err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDiscoveryGatewayPolicy serves GET/PUT/DELETE at one gateway scope.
func (h *Hub) handleDiscoveryGatewayPolicy(w http.ResponseWriter, r *http.Request) {
	if h.discoveryPolicy == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "DISCOVERY_POLICY_UNAVAILABLE",
			"discovery policy management is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	gateway := r.PathValue("gateway")
	if gateway == "" {
		writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_INVALID", "gateway is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		view, err := h.discoveryPolicy.EffectiveView(r.Context(), tenant, gateway)
		if err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, view)
	case http.MethodPut:
		var body discovery.Override
		if !decodeBody(w, r, &body) {
			return
		}
		if overrideEmpty(body) {
			writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_EMPTY",
				"provide at least one of enabled, max_per_cycle, max_candidates, default_profile")
			return
		}
		view, err := h.discoveryPolicy.SetGatewayPolicy(r.Context(), tenant, gateway, body)
		if err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		if err := h.discoveryPolicy.DeleteGatewayPolicy(r.Context(), tenant, gateway); err != nil {
			writeDiscoveryPolicyError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// overrideEmpty reports that no field is set, i.e. the PUT would create a
// row that inherits everything (a no-op).
func overrideEmpty(o discovery.Override) bool {
	return o.Enabled == nil && o.MaxPerCycle == nil &&
		o.MaxCandidates == nil && o.DefaultProfile == nil &&
		o.ApprovalMode == nil
}

func writeDiscoveryPolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discovery.ErrInvalidCap):
		writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_INVALID_CAP", err.Error())
	case errors.Is(err, discovery.ErrInvalidApprovalMode):
		writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_INVALID_APPROVAL_MODE", err.Error())
	case errors.Is(err, discovery.ErrGatewayUnknown):
		writeRESTError(w, http.StatusBadRequest, "DISCOVERY_POLICY_GATEWAY_UNKNOWN", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "DISCOVERY_POLICY_OPERATION_FAILED", err.Error())
	}
}
