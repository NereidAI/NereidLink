// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// business_handlers.go exposes immutable business-template versions and
// generation-fenced instance workflows. The handlers translate HTTP only;
// deterministic expansion and destination-owner coordination stay in the
// business module.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/nereidai/nereidlink/internal/business"
)

// BusinessAdmin is the REST surface of the business orchestration module.
type BusinessAdmin interface {
	ListTemplates(ctx context.Context, tenantID string) ([]business.Template, error)
	CreateTemplate(ctx context.Context, tenantID, actor string, in business.Template) (business.Template, error)
	ImportTemplates(ctx context.Context, tenantID, actor string, raw []byte) ([]business.Template, error)
	ExportTemplates(ctx context.Context, tenantID string) (business.TemplatePack, error)
	DeleteTemplate(ctx context.Context, tenantID, templateID, version string) error
	ListInstances(ctx context.Context, tenantID string) ([]business.Instance, error)
	CreateInstance(ctx context.Context, tenantID string, in business.InstanceInput) (business.Instance, error)
	GetInstance(ctx context.Context, tenantID, instanceID string) (business.Instance, error)
	UpdateInstance(ctx context.Context, tenantID, instanceID string, in business.InstanceUpdate) (business.Instance, error)
	DeleteInstance(ctx context.Context, tenantID, instanceID string) error
	ValidateInstance(ctx context.Context, tenantID, instanceID string) (business.ValidationReport, error)
	Expansion(ctx context.Context, tenantID, instanceID string, generation int64) (business.Expansion, error)
	Deploy(ctx context.Context, tenantID, instanceID string) (business.Instance, error)
	Undeploy(ctx context.Context, tenantID, instanceID string) (business.Instance, error)
	Rollback(ctx context.Context, tenantID, instanceID string, generation int64) (business.Instance, error)
	Monitor(ctx context.Context, tenantID, instanceID string) (business.Monitor, error)
}

// SetBusinessAdmin wires the business orchestration module.
func (h *Hub) SetBusinessAdmin(admin BusinessAdmin) {
	h.businessAdmin = admin
}

// businessCallReady refuses requests when the composition root omitted the
// module instead of silently falling back to another resource owner.
func (h *Hub) businessCallReady(w http.ResponseWriter) (BusinessAdmin, bool) {
	if h.businessAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "BUSINESS_SERVICE_UNAVAILABLE",
			"business orchestration is not enabled")
		return nil, false
	}
	return h.businessAdmin, true
}

// handleBusinessTemplates lists imported versions or creates one immutable
// template version. Structural validation stays in the domain module.
func (h *Hub) handleBusinessTemplates(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		templates, err := admin.ListTemplates(r.Context(), tenant)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, templates)
	case http.MethodPost:
		var in business.Template
		if !decodeBody(w, r, &in) {
			return
		}
		created, err := admin.CreateTemplate(r.Context(), tenant, forwarderActor(r), in)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, created)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleBusinessTemplateVersion deletes an unreferenced custom version. The
// store fences concurrent references.
func (h *Hub) handleBusinessTemplateVersion(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := admin.DeleteTemplate(r.Context(), r.PathValue("tenant"),
		r.PathValue("template"), r.PathValue("version")); err != nil {
		writeBusinessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxTemplatePackBytes bounds an imported template-pack body. Packs are
// declarative YAML/JSON documents, so one megabyte is ample.
const maxTemplatePackBytes = 1 << 20

// handleBusinessTemplateImport accepts a YAML or JSON template pack — a bare
// template document or a {"templates":[...]} envelope — and imports every
// version atomically as the calling actor.
func (h *Hub) handleBusinessTemplateImport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplatePackBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "BUSINESS_TEMPLATE_PACK_INVALID",
			"template pack is too large or unreadable")
		return
	}
	imported, err := admin.ImportTemplates(r.Context(), r.PathValue("tenant"),
		forwarderActor(r), raw)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, imported)
}

// handleBusinessTemplateExport streams every tenant template as a portable
// pack. YAML is the default wire format; ?format=json returns the same
// envelope as JSON.
func (h *Hub) handleBusinessTemplateExport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	pack, err := admin.ExportTemplates(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "yaml"
	}
	var (
		body        []byte
		contentType string
		filename    string
	)
	switch format {
	case "yaml":
		body, err = yaml.Marshal(pack)
		contentType = "application/yaml; charset=utf-8"
		filename = "business-templates.yaml"
	case "json":
		body, err = json.MarshalIndent(pack, "", "  ")
		contentType = "application/json"
		filename = "business-templates.json"
	default:
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"format must be yaml or json")
		return
	}
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "BUSINESS_OPERATION_FAILED", err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleBusinessInstances lists or creates deployment instances.
func (h *Hub) handleBusinessInstances(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		instances, err := admin.ListInstances(r.Context(), tenant)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, instances)
	case http.MethodPost:
		var in business.InstanceInput
		if !decodeBody(w, r, &in) {
			return
		}
		created, err := admin.CreateInstance(r.Context(), tenant, in)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, created)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleBusinessInstance reads, generation-fences an update, or deletes one
// instance. The store enforces the update generation, not the HTTP handler.
func (h *Hub) handleBusinessInstance(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	tenant, id := r.PathValue("tenant"), r.PathValue("instance")
	switch r.Method {
	case http.MethodGet:
		instance, err := admin.GetInstance(r.Context(), tenant, id)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, instance)
	case http.MethodPut:
		var in business.InstanceUpdate
		if !decodeBody(w, r, &in) {
			return
		}
		updated, err := admin.UpdateInstance(r.Context(), tenant, id, in)
		if err != nil {
			writeBusinessError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := admin.DeleteInstance(r.Context(), tenant, id); err != nil {
			writeBusinessError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleBusinessValidate evaluates the current document and live model gates.
func (h *Hub) handleBusinessValidate(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	report, err := admin.ValidateInstance(r.Context(), r.PathValue("tenant"), r.PathValue("instance"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, report)
}

// handleBusinessExpansion returns one immutable expansion snapshot. Generation
// zero selects the latest desired generation.
func (h *Hub) handleBusinessExpansion(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	var generation int64
	if raw := r.URL.Query().Get("generation"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeRESTError(w, http.StatusBadRequest, "BUSINESS_GENERATION_INVALID",
				"generation must be a positive integer")
			return
		}
		generation = parsed
	}
	expansion, err := admin.Expansion(r.Context(), r.PathValue("tenant"),
		r.PathValue("instance"), generation)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, expansion)
}

// handleBusinessDeploy validates and applies the latest desired generation.
func (h *Hub) handleBusinessDeploy(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	instance, err := admin.Deploy(r.Context(), r.PathValue("tenant"), r.PathValue("instance"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, instance)
}

// handleBusinessUndeploy removes only resources owned by the active snapshot.
func (h *Hub) handleBusinessUndeploy(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	instance, err := admin.Undeploy(r.Context(), r.PathValue("tenant"), r.PathValue("instance"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, instance)
}

// businessRollbackBody selects a retained expansion snapshot.
type businessRollbackBody struct {
	Generation int64 `json:"generation"`
}

// handleBusinessRollback re-applies a retained generation through the same
// preflight and resource-owner path as a normal deployment.
func (h *Hub) handleBusinessRollback(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	var body businessRollbackBody
	if !decodeBody(w, r, &body) {
		return
	}
	instance, err := admin.Rollback(r.Context(), r.PathValue("tenant"),
		r.PathValue("instance"), body.Generation)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, instance)
}

// handleBusinessMonitor reads current health without turning missing NI job
// metrics into fabricated zero values.
func (h *Hub) handleBusinessMonitor(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.businessCallReady(w)
	if !ok {
		return
	}
	monitor, err := admin.Monitor(r.Context(), r.PathValue("tenant"), r.PathValue("instance"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, monitor)
}

// writeBusinessError maps domain conflicts and validation failures without
// leaking PostgreSQL implementation details into the API contract.
func writeBusinessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, business.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "BUSINESS_RESOURCE_NOT_FOUND", err.Error())
	case errors.Is(err, business.ErrGenerationConflict):
		writeRESTError(w, http.StatusConflict, "BUSINESS_GENERATION_CONFLICT", err.Error())
	case errors.Is(err, business.ErrConflict):
		writeRESTError(w, http.StatusConflict, "BUSINESS_RESOURCE_CONFLICT", err.Error())
	case errors.Is(err, business.ErrInvalid):
		writeRESTError(w, http.StatusBadRequest, "BUSINESS_DOCUMENT_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "BUSINESS_OPERATION_FAILED", err.Error())
	}
}
