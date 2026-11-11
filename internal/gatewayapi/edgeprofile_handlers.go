// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// edgeprofile_handlers.go exposes gateway-reported EdgeX profiles over tenant
// REST: a read-only list of cached profiles for one gateway and an explicit
// adopt action that creates/updates a native device profile. Listing requires
// profile:read and adopting requires profile:write (see gatewayPoint). The
// cached content is never mutated by these reads; adoption is an explicit
// operator decision rather than an automatic side effect.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/edgeprofile"
	"github.com/nereidai/nereidlink/internal/registry"
)

// EdgeProfileAdmin is the edge profile cache/adopt/import surface.
type EdgeProfileAdmin interface {
	ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]edgeprofile.CachedProfile, error)
	Detail(ctx context.Context, tenantID, gatewayID, name string) (edgeprofile.Detail, error)
	Adopt(ctx context.Context, tenantID string, req edgeprofile.AdoptRequest) (edgeprofile.AdoptResult, error)
	ImportDocument(ctx context.Context, tenantID string, raw []byte) (edgeprofile.ImportResult, error)
	PreviewDocument(raw []byte) (edgeprofile.ImportResult, error)
}

// edgeProfileDTO is the read-only projection of one cached edge profile.
type edgeProfileDTO struct {
	Name             string     `json:"name"`
	ContentHash      string     `json:"content_hash"`
	ResourceCount    int        `json:"resource_count"`
	FirstSeenAt      time.Time  `json:"first_seen_at"`
	LastSeenAt       time.Time  `json:"last_seen_at"`
	AdoptedProfileID string     `json:"adopted_profile_id,omitempty"`
	AdoptedAt        *time.Time `json:"adopted_at,omitempty"`
}

// adoptEdgeProfileRequest is the adopt body. All fields are optional.
type adoptEdgeProfileRequest struct {
	Mode       string `json:"mode"`
	TargetName string `json:"target_name"`
}

// skippedResourceDTO mirrors edgeprofile.SkippedResource on the wire.
type skippedResourceDTO struct {
	Name      string `json:"name"`
	ValueType string `json:"value_type"`
	Reason    string `json:"reason"`
}

// adoptEdgeProfileResponse reports the adopt outcome.
type adoptEdgeProfileResponse struct {
	ProfileID    string               `json:"profile_id"`
	ProfileName  string               `json:"profile_name"`
	Created      bool                 `json:"created"`
	Updated      bool                 `json:"updated"`
	AdoptedCount int                  `json:"adopted_count"`
	Skipped      []skippedResourceDTO `json:"skipped"`
}

// SetEdgeProfileAdmin wires the edge profile cache/adopt service.
func (h *Hub) SetEdgeProfileAdmin(admin EdgeProfileAdmin) {
	h.edgeProfiles = admin
}

// importProfileRequest is the paste-import body. Document carries either the
// vendor-neutral NL profile shape or an EdgeX DeviceProfile JSON.
type importProfileRequest struct {
	Document json.RawMessage `json:"document"`
}

// importProfileResponse reports a preview or a stored import.
type importProfileResponse struct {
	ProfileID  string   `json:"profile_id,omitempty"`
	PointCount int      `json:"point_count"`
	Warnings   []string `json:"warnings"`
}

// handleProfileImportPreview parses a pasted document without persisting.
func (h *Hub) handleProfileImportPreview(w http.ResponseWriter, r *http.Request) {
	if h.edgeProfiles == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "EDGE_PROFILE_SERVICE_UNAVAILABLE",
			"profile import service is not configured")
		return
	}
	var req importProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Document) == 0 {
		writeRESTError(w, http.StatusBadRequest, "PROFILE_INVALID",
			"request body must contain a profile document")
		return
	}
	res, err := h.edgeProfiles.PreviewDocument(req.Document)
	if err != nil {
		writeEdgeProfileError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, importProfileResponse{
		PointCount: res.PointCount,
		Warnings:   orEmptyStrings(res.Warnings),
	})
}

// handleProfileImport parses and stores a pasted document as a source=import
// library profile.
func (h *Hub) handleProfileImport(w http.ResponseWriter, r *http.Request) {
	if h.edgeProfiles == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "EDGE_PROFILE_SERVICE_UNAVAILABLE",
			"profile import service is not configured")
		return
	}
	var req importProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Document) == 0 {
		writeRESTError(w, http.StatusBadRequest, "PROFILE_INVALID",
			"request body must contain a profile document")
		return
	}
	res, err := h.edgeProfiles.ImportDocument(r.Context(), r.PathValue("tenant"), req.Document)
	if err != nil {
		writeEdgeProfileError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, importProfileResponse{
		ProfileID:  res.ProfileID,
		PointCount: res.PointCount,
		Warnings:   orEmptyStrings(res.Warnings),
	})
}

// orEmptyStrings guarantees arrays are emitted as [] rather than null.
func orEmptyStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// edgeProfileResourceDTO is one raw deviceResource with its adopt decision.
type edgeProfileResourceDTO struct {
	Name         string `json:"name"`
	ValueType    string `json:"value_type"`
	Units        string `json:"units"`
	Description  string `json:"description"`
	Translatable bool   `json:"translatable"`
	SkipReason   string `json:"skip_reason,omitempty"`
}

// edgeProfileDetailDTO is the full cached profile: metadata plus resources.
type edgeProfileDetailDTO struct {
	edgeProfileDTO
	Resources []edgeProfileResourceDTO `json:"resources"`
}

// handleEdgeProfileDetail returns one cached profile with its raw resources.
func (h *Hub) handleEdgeProfileDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.edgeProfiles == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "EDGE_PROFILE_SERVICE_UNAVAILABLE",
			"edge profile management is not enabled")
		return
	}
	detail, err := h.edgeProfiles.Detail(r.Context(), r.PathValue("tenant"),
		r.PathValue("gateway"), r.PathValue("profile"))
	if err != nil {
		writeEdgeProfileError(w, err)
		return
	}
	out := edgeProfileDetailDTO{
		edgeProfileDTO: edgeProfileDTO{
			Name:             detail.Cached.Name,
			ContentHash:      detail.Cached.ContentHash,
			ResourceCount:    detail.Cached.ResourceCount,
			FirstSeenAt:      detail.Cached.FirstSeenAt,
			LastSeenAt:       detail.Cached.LastSeenAt,
			AdoptedProfileID: detail.Cached.AdoptedProfileID,
			AdoptedAt:        detail.Cached.AdoptedAt,
		},
		Resources: make([]edgeProfileResourceDTO, 0, len(detail.Resources)),
	}
	for _, rc := range detail.Resources {
		out.Resources = append(out.Resources, edgeProfileResourceDTO{
			Name:         rc.Resource.Name,
			ValueType:    rc.Resource.ValueType,
			Units:        rc.Resource.Units,
			Description:  rc.Resource.Description,
			Translatable: rc.Translatable,
			SkipReason:   rc.SkipReason,
		})
	}
	writeRESTJSON(w, http.StatusOK, out)
}

// handleEdgeProfiles lists EdgeX profiles cached for one gateway.
func (h *Hub) handleEdgeProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.edgeProfiles == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "EDGE_PROFILE_SERVICE_UNAVAILABLE",
			"edge profile management is not enabled")
		return
	}
	list, err := h.edgeProfiles.ListForGateway(r.Context(),
		r.PathValue("tenant"), r.PathValue("gateway"))
	if err != nil {
		writeEdgeProfileError(w, err)
		return
	}
	out := make([]edgeProfileDTO, 0, len(list))
	for _, p := range list {
		out = append(out, edgeProfileDTO{
			Name:             p.Name,
			ContentHash:      p.ContentHash,
			ResourceCount:    p.ResourceCount,
			FirstSeenAt:      p.FirstSeenAt,
			LastSeenAt:       p.LastSeenAt,
			AdoptedProfileID: p.AdoptedProfileID,
			AdoptedAt:        p.AdoptedAt,
		})
	}
	writeRESTJSON(w, http.StatusOK, out)
}

// handleEdgeProfileAdopt adopts one cached profile into a native profile.
func (h *Hub) handleEdgeProfileAdopt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.edgeProfiles == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "EDGE_PROFILE_SERVICE_UNAVAILABLE",
			"edge profile management is not enabled")
		return
	}
	var body adoptEdgeProfileRequest
	if r.ContentLength != 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	result, err := h.edgeProfiles.Adopt(r.Context(), r.PathValue("tenant"),
		edgeprofile.AdoptRequest{
			GatewayID:  r.PathValue("gateway"),
			Name:       r.PathValue("profile"),
			Mode:       edgeprofile.AdoptMode(body.Mode),
			TargetName: body.TargetName,
		})
	if err != nil {
		writeEdgeProfileError(w, err)
		return
	}
	skipped := make([]skippedResourceDTO, 0, len(result.Skipped))
	for _, s := range result.Skipped {
		skipped = append(skipped, skippedResourceDTO{
			Name: s.Name, ValueType: s.ValueType, Reason: s.Reason,
		})
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeRESTJSON(w, status, adoptEdgeProfileResponse{
		ProfileID:    result.ProfileID,
		ProfileName:  result.ProfileName,
		Created:      result.Created,
		Updated:      result.Updated,
		AdoptedCount: result.AdoptedCount,
		Skipped:      skipped,
	})
}

// writeEdgeProfileError maps edge profile and registry errors to HTTP codes.
func writeEdgeProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, edgeprofile.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "EDGE_PROFILE_NOT_FOUND", err.Error())
	case errors.Is(err, edgeprofile.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "EDGE_PROFILE_ADOPT_INVALID", err.Error())
	case errors.Is(err, registry.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "PROFILE_INVALID", err.Error())
	case errors.Is(err, registry.ErrConflict):
		writeRESTError(w, http.StatusConflict, "PROFILE_NAME_CONFLICT", err.Error())
	case errors.Is(err, registry.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "PROFILE_NOT_FOUND", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "EDGE_PROFILE_OPERATION_FAILED", err.Error())
	}
}
