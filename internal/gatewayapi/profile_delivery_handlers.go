// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profile_delivery_handlers.go exposes device-profile downlink over tenant
// REST (ADR-0038). Two surfaces are provided: the profile-centric distribution
// list (which gateways should receive a library profile) and the gateway-centric
// delivery state (how far each profile got on one gateway). Routes are mapped
// to profile:read / profile:write in authorize.go.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/profilepush"
)

// ProfileDeliveryAdmin is the device-profile downlink administration surface.
type ProfileDeliveryAdmin interface {
	// Distribute adds an explicit distribution edge (library profile ->
	// gateway) and republishes the gateway.
	Distribute(ctx context.Context, tenantID, profileID, gatewayID string) error
	// WithdrawTarget removes an explicit distribution edge, sending a
	// removal frame when the gateway no longer needs the profile.
	WithdrawTarget(ctx context.Context, tenantID, profileID, gatewayID string) error
	// Targets lists a profile's explicit distribution edges.
	Targets(ctx context.Context, tenantID, profileID string) ([]profilepush.Target, error)
	// DeliveriesForGateway lists a gateway's per-profile delivery states.
	DeliveriesForGateway(ctx context.Context, tenantID, gatewayID string) ([]profilepush.Delivery, error)
	// DeliveriesForProfile lists a profile's delivery states across gateways.
	DeliveriesForProfile(ctx context.Context, tenantID, profileID string) ([]profilepush.Delivery, error)
}

// SetProfileDeliveryAdmin wires the device-profile downlink service.
func (h *Hub) SetProfileDeliveryAdmin(admin ProfileDeliveryAdmin) {
	h.profileDeliveryAdmin = admin
}

// profileTargetBody is the create request body for a distribution edge.
type profileTargetBody struct {
	GatewayID string `json:"gateway_id"`
}

// handleProfileTargets lists or creates the explicit distribution edges for
// one library profile.
//
//	GET  /v1/tenants/{tenant}/profiles/{profile}/targets
//	POST /v1/tenants/{tenant}/profiles/{profile}/targets
func (h *Hub) handleProfileTargets(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.profileDeliveryCallReady(w)
	if !ok {
		return
	}
	tenant, profile := r.PathValue("tenant"), r.PathValue("profile")
	switch r.Method {
	case http.MethodGet:
		targets, err := admin.Targets(r.Context(), tenant, profile)
		if err != nil {
			writeProfileDeliveryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, targets)
	case http.MethodPost:
		var body profileTargetBody
		if !decodeBody(w, r, &body) {
			return
		}
		if err := admin.Distribute(r.Context(), tenant, profile, body.GatewayID); err != nil {
			writeProfileDeliveryError(w, err)
			return
		}
		targets, err := admin.Targets(r.Context(), tenant, profile)
		if err != nil {
			writeProfileDeliveryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, targets)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleProfileTarget deletes one explicit distribution edge by gateway id.
//
//	DELETE /v1/tenants/{tenant}/profiles/{profile}/targets/{gateway}
func (h *Hub) handleProfileTarget(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.profileDeliveryCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tenant, profile, gateway := r.PathValue("tenant"), r.PathValue("profile"), r.PathValue("gateway")
	if err := admin.WithdrawTarget(r.Context(), tenant, profile, gateway); err != nil {
		writeProfileDeliveryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProfileDeliveries lists the delivery states for one library profile
// across gateways.
//
//	GET /v1/tenants/{tenant}/profiles/{profile}/deliveries
func (h *Hub) handleProfileDeliveries(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.profileDeliveryCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tenant, profile := r.PathValue("tenant"), r.PathValue("profile")
	deliveries, err := admin.DeliveriesForProfile(r.Context(), tenant, profile)
	if err != nil {
		writeProfileDeliveryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, deliveries)
}

// handleGatewayProfileDeliveries lists the delivery states for one gateway
// across profiles (the gateway detail "Profiles" tab).
//
//	GET /v1/tenants/{tenant}/gateways/{gateway}/profile-deliveries
func (h *Hub) handleGatewayProfileDeliveries(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.profileDeliveryCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tenant, gateway := r.PathValue("tenant"), r.PathValue("gateway")
	deliveries, err := admin.DeliveriesForGateway(r.Context(), tenant, gateway)
	if err != nil {
		writeProfileDeliveryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, deliveries)
}

// profileDeliveryCallReady verifies the downlink service is wired.
func (h *Hub) profileDeliveryCallReady(w http.ResponseWriter) (ProfileDeliveryAdmin, bool) {
	if h.profileDeliveryAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "PROFILE_DELIVERY_UNAVAILABLE",
			"device-profile downlink is not enabled")
		return nil, false
	}
	return h.profileDeliveryAdmin, true
}

// writeProfileDeliveryError maps downlink errors to HTTP status codes.
func writeProfileDeliveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, profilepush.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "PROFILE_DELIVERY_NOT_FOUND", err.Error())
	case errors.Is(err, profilepush.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "PROFILE_DELIVERY_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "PROFILE_DELIVERY_FAILED", err.Error())
	}
}
