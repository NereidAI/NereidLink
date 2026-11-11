// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// companion_handlers.go exposes the platform-neutral companion connector
// administration surface: endpoint registration and the desired/read-back
// subscription rows. The companion auth token is write-only: responses
// report only whether one is stored, never the secret itself.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/companion"
)

// CompanionAdmin is the companion endpoint management surface.
type CompanionAdmin interface {
	ListEndpoints(ctx context.Context, tenantID string) ([]companion.Endpoint, error)
	GetEndpoint(ctx context.Context, tenantID, endpointID string) (companion.Endpoint, error)
	Register(ctx context.Context, tenantID string, in companion.EndpointInput) (companion.Endpoint, error)
	Update(ctx context.Context, tenantID, endpointID string, in companion.EndpointInput) (companion.Endpoint, error)
	Delete(ctx context.Context, tenantID, endpointID string) error
	ListDesired(ctx context.Context, tenantID, endpointID string) ([]companion.DesiredRow, error)
}

// SetCompanionAdmin wires the companion management service.
func (h *Hub) SetCompanionAdmin(admin CompanionAdmin) {
	h.companionAdmin = admin
}

// requireCompanionAdmin returns the service or writes a 503.
func (h *Hub) requireCompanionAdmin(w http.ResponseWriter) CompanionAdmin {
	if h.companionAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMPANION_SERVICE_UNAVAILABLE",
			"companion connector is not enabled")
		return nil
	}
	return h.companionAdmin
}

// companionEndpointBody is the create/update request. AuthToken is a pointer
// so an update can distinguish omitted (keep) from explicit empty (clear).
type companionEndpointBody struct {
	CompanionID        string  `json:"companion_id"`
	GatewayID          string  `json:"gateway_id"`
	BaseURL            string  `json:"base_url"`
	ContractVersion    string  `json:"contract_version"`
	AuthToken          *string `json:"auth_token"`
	InsecureSkipVerify bool    `json:"insecure_skip_verify"`
	Enabled            *bool   `json:"enabled"`
}

func (b companionEndpointBody) toInput() companion.EndpointInput {
	return companion.EndpointInput{
		CompanionID:        b.CompanionID,
		GatewayID:          b.GatewayID,
		BaseURL:            b.BaseURL,
		ContractVersion:    b.ContractVersion,
		AuthToken:          b.AuthToken,
		InsecureSkipVerify: b.InsecureSkipVerify,
		Enabled:            b.Enabled,
	}
}

// companionEndpointView is the response shape. AuthTokenSet replaces the
// secret so it can never be read back through the API.
type companionEndpointView struct {
	ID                 string    `json:"id"`
	CompanionID        string    `json:"companion_id"`
	GatewayID          string    `json:"gateway_id"`
	BaseURL            string    `json:"base_url"`
	ContractVersion    string    `json:"contract_version"`
	AuthTokenSet       bool      `json:"auth_token_set"`
	InsecureSkipVerify bool      `json:"insecure_skip_verify"`
	Enabled            bool      `json:"enabled"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func companionView(ep companion.Endpoint) companionEndpointView {
	return companionEndpointView{
		ID:                 ep.ID,
		CompanionID:        ep.CompanionID,
		GatewayID:          ep.GatewayID,
		BaseURL:            ep.BaseURL,
		ContractVersion:    ep.ContractVersion,
		AuthTokenSet:       ep.AuthToken != "",
		InsecureSkipVerify: ep.InsecureSkipVerify,
		Enabled:            ep.Enabled,
		CreatedAt:          ep.CreatedAt,
		UpdatedAt:          ep.UpdatedAt,
	}
}

type companionDesiredView struct {
	RowID              string `json:"id"`
	SourceDevice       string `json:"source_device"`
	Camera             string `json:"camera"`
	PipelineID         string `json:"pipeline_id"`
	ConclusionDeviceID string `json:"conclusion_device_id"`
	Enabled            bool   `json:"enabled"`
	ArtifactKey        string `json:"artifact_key"`
	Revision           int64  `json:"revision"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
}

// handleCompanionEndpoints dispatches the collection routes.
func (h *Hub) handleCompanionEndpoints(w http.ResponseWriter, r *http.Request) {
	admin := h.requireCompanionAdmin(w)
	if admin == nil {
		return
	}
	tenant := r.PathValue("tenant")

	if r.Method == http.MethodPost {
		var body companionEndpointBody
		if !decodeBody(w, r, &body) {
			return
		}
		ep, err := admin.Register(r.Context(), tenant, body.toInput())
		if h.writeCompanionError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusCreated, companionView(ep))
		return
	}

	list, err := admin.ListEndpoints(r.Context(), tenant)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "COMPANION_LIST_FAILED", err.Error())
		return
	}
	views := make([]companionEndpointView, 0, len(list))
	for _, ep := range list {
		views = append(views, companionView(ep))
	}
	writeRESTJSON(w, http.StatusOK, views)
}

// handleCompanionEndpoint dispatches one-endpoint routes.
func (h *Hub) handleCompanionEndpoint(w http.ResponseWriter, r *http.Request) {
	admin := h.requireCompanionAdmin(w)
	if admin == nil {
		return
	}
	tenant := r.PathValue("tenant")
	id := r.PathValue("endpoint")

	switch r.Method {
	case http.MethodGet:
		ep, err := admin.GetEndpoint(r.Context(), tenant, id)
		if h.writeCompanionError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusOK, companionView(ep))

	case http.MethodPut:
		var body companionEndpointBody
		if !decodeBody(w, r, &body) {
			return
		}
		ep, err := admin.Update(r.Context(), tenant, id, body.toInput())
		if h.writeCompanionError(w, err) {
			return
		}
		writeRESTJSON(w, http.StatusOK, companionView(ep))

	case http.MethodDelete:
		if err := admin.Delete(r.Context(), tenant, id); h.writeCompanionError(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCompanionSubscriptions lists the desired rows and read-back status
// for one endpoint.
func (h *Hub) handleCompanionSubscriptions(w http.ResponseWriter, r *http.Request) {
	admin := h.requireCompanionAdmin(w)
	if admin == nil {
		return
	}
	rows, err := admin.ListDesired(r.Context(), r.PathValue("tenant"),
		r.PathValue("endpoint"))
	if h.writeCompanionError(w, err) {
		return
	}
	views := make([]companionDesiredView, 0, len(rows))
	for _, row := range rows {
		views = append(views, companionDesiredView{
			RowID:              row.RowID,
			SourceDevice:       row.SourceDevice,
			Camera:             row.Camera,
			PipelineID:         row.PipelineID,
			ConclusionDeviceID: row.ConclusionDeviceID,
			Enabled:            row.Enabled,
			ArtifactKey:        row.ArtifactKey,
			Revision:           row.Revision,
			Status:             row.Status,
			Reason:             row.Reason,
		})
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"subscriptions": views})
}

// writeCompanionError maps companion errors onto REST errors.
func (h *Hub) writeCompanionError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, companion.ErrEndpointNotFound):
		writeRESTError(w, http.StatusNotFound, "COMPANION_ENDPOINT_NOT_FOUND", err.Error())
	case errors.Is(err, companion.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "COMPANION_ENDPOINT_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "COMPANION_OPERATION_FAILED", err.Error())
	}
	return true
}
