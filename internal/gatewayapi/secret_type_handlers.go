// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secret_type_handlers.go exposes the edge secret type descriptor catalog
// over tenant REST (ADR-0034). Edge-advertised descriptors are read-only;
// tenant-defined descriptors are full CRUD, and an edge descriptor can be
// cloned into an editable manual one. Reads need edge_secret:read, writes
// edge_secret:write (mapped in authorize.go).

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/secretcatalog"
)

// SecretTypeAdmin is the descriptor management surface.
type SecretTypeAdmin interface {
	List(ctx context.Context, tenantID string) ([]secretcatalog.SecretType, error)
	Get(ctx context.Context, tenantID, secretType, version string) (secretcatalog.SecretType, error)
	CreateManual(ctx context.Context, tenantID string, reg secretcatalog.Registration) (secretcatalog.SecretType, error)
	UpdateManual(ctx context.Context, tenantID, secretType, version string, reg secretcatalog.Registration) (secretcatalog.SecretType, error)
	DeleteManual(ctx context.Context, tenantID, secretType, version string) error
	CloneToManual(ctx context.Context, tenantID, secretType, version, newType string) (secretcatalog.SecretType, error)
}

// SetSecretTypeAdmin wires the descriptor catalog.
func (h *Hub) SetSecretTypeAdmin(admin SecretTypeAdmin) {
	h.secretTypes = admin
}

// secretTypeBody carries one descriptor write.
type secretTypeBody struct {
	Type         string            `json:"type"`
	TypeVersion  string            `json:"type_version"`
	FieldsSchema map[string]any    `json:"fields_schema"`
	DisplayName  map[string]string `json:"display_name,omitempty"`
}

// toRegistration converts the request body into the catalog registration.
func (b secretTypeBody) toRegistration() (secretcatalog.Registration, bool) {
	raw, err := encodeObject(b.FieldsSchema)
	if err != nil {
		return secretcatalog.Registration{}, false
	}
	return secretcatalog.Registration{
		SecretType:   b.Type,
		TypeVersion:  b.TypeVersion,
		FieldsSchema: raw,
		DisplayName:  b.DisplayName,
	}, true
}

// handleSecretTypes lists descriptors and accepts a new manual descriptor.
func (h *Hub) handleSecretTypes(w http.ResponseWriter, r *http.Request) {
	if h.secretTypes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_TYPE_CATALOG_UNAVAILABLE",
			"edge secret type catalog is not enabled")
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := h.secretTypes.List(r.Context(), r.PathValue("tenant"))
		if err != nil {
			writeSecretTypeError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var body secretTypeBody
		if !decodeBody(w, r, &body) {
			return
		}
		reg, ok := body.toRegistration()
		if !ok {
			writeRESTError(w, http.StatusBadRequest, "SECRET_TYPE_INVALID_SCHEMA",
				"fields_schema must be a JSON Schema object")
			return
		}
		st, err := h.secretTypes.CreateManual(r.Context(), r.PathValue("tenant"), reg)
		if err != nil {
			writeSecretTypeError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, st)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// secretTypeCloneBody carries the target key for a clone request.
type secretTypeCloneBody struct {
	NewType string `json:"new_type"`
}

// handleSecretType serves GET, PUT and DELETE for one descriptor, plus POST
// to clone an edge descriptor into a manual one. The version query parameter
// selects non-default versions.
func (h *Hub) handleSecretType(w http.ResponseWriter, r *http.Request) {
	if h.secretTypes == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_TYPE_CATALOG_UNAVAILABLE",
			"edge secret type catalog is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	secretType := r.PathValue("type")
	version := r.URL.Query().Get("version")
	switch r.Method {
	case http.MethodGet:
		st, err := h.secretTypes.Get(r.Context(), tenant, secretType, version)
		if err != nil {
			writeSecretTypeError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, st)
	case http.MethodPut:
		var body secretTypeBody
		if !decodeBody(w, r, &body) {
			return
		}
		reg, ok := body.toRegistration()
		if !ok {
			writeRESTError(w, http.StatusBadRequest, "SECRET_TYPE_INVALID_SCHEMA",
				"fields_schema must be a JSON Schema object")
			return
		}
		st, err := h.secretTypes.UpdateManual(r.Context(), tenant, secretType, version, reg)
		if err != nil {
			writeSecretTypeError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, st)
	case http.MethodDelete:
		if err := h.secretTypes.DeleteManual(r.Context(), tenant, secretType, version); err != nil {
			writeSecretTypeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		var body secretTypeCloneBody
		if !decodeBody(w, r, &body) {
			return
		}
		st, err := h.secretTypes.CloneToManual(r.Context(), tenant, secretType, version, body.NewType)
		if err != nil {
			writeSecretTypeError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, st)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// encodeObject marshals a schema object, rejecting a nil schema.
func encodeObject(schema map[string]any) (raw []byte, err error) {
	if len(schema) == 0 {
		return nil, errors.New("fields schema is required")
	}
	return json.Marshal(schema)
}

// writeSecretTypeError maps catalog errors to the REST error envelope.
func writeSecretTypeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secretcatalog.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "SECRET_TYPE_NOT_FOUND",
			"edge secret type not found")
	case errors.Is(err, secretcatalog.ErrConflict):
		writeRESTError(w, http.StatusConflict, "SECRET_TYPE_CONFLICT",
			"an edge secret type with that key already exists")
	case errors.Is(err, secretcatalog.ErrInvalidSchema):
		writeRESTError(w, http.StatusBadRequest, "SECRET_TYPE_INVALID_SCHEMA", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "SECRET_TYPE_OPERATION_FAILED", err.Error())
	}
}
