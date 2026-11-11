// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ca_handlers.go exposes platform trust-anchor management to platform
// administrators (ADR-0009): list, import, enable/disable and delete of
// CA certificates, plus the public CA bundle endpoint used to provision
// devices. Only the built-in CA can issue certificates; imported CAs
// are trust anchors whose devices authenticate with certificates
// issued elsewhere.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/pki"
)

// writeCAError maps trust-anchor errors to HTTP status codes.
func writeCAError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pki.ErrCANotFound):
		writeRESTError(w, http.StatusNotFound, "CA_NOT_FOUND", err.Error())
	case errors.Is(err, pki.ErrDuplicateCA):
		writeRESTError(w, http.StatusConflict, "CA_DUPLICATE", err.Error())
	case errors.Is(err, pki.ErrInvalidCA):
		writeRESTError(w, http.StatusBadRequest, "CA_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "CA_OPERATION_FAILED", err.Error())
	}
}

// TrustAnchorManager is the CA management surface, implemented by
// *pki.Manager.
type TrustAnchorManager interface {
	// ListCAs returns every platform trust anchor.
	ListCAs(ctx context.Context) ([]pki.CARecord, error)

	// ImportCA validates and stores a third-party CA certificate.
	ImportCA(ctx context.Context, label, rawPEM string) (pki.CARecord, error)

	// SetCAStatus enables or disables a trust anchor.
	SetCAStatus(ctx context.Context, id, status string) error

	// DeleteCA removes a non-builtin trust anchor.
	DeleteCA(ctx context.Context, id string) error

	// ActiveBundle returns the concatenated PEM of active anchors.
	ActiveBundle(ctx context.Context) (string, error)
}

// importCABody carries an import request.
type importCABody struct {
	// Label is the display name.
	Label string `json:"label"`

	// PEM is the CA certificate PEM.
	PEM string `json:"pem"`
}

// caStatusBody carries an enable/disable request.
type caStatusBody struct {
	// Status is ACTIVE or DISABLED.
	Status string `json:"status"`
}

// SetTrustAnchorManager wires the platform CA manager.
func (h *Hub) SetTrustAnchorManager(manager TrustAnchorManager) {
	h.trustAnchorManager = manager
}

// handlePlatformCAs lists or imports trust anchors.
func (h *Hub) handlePlatformCAs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		records, err := h.trustAnchorManager.ListCAs(r.Context())
		if err != nil {
			writeRESTError(w, http.StatusInternalServerError, "CA_LIST_FAILED", err.Error())
			return
		}
		writeRESTJSON(w, http.StatusOK, records)
	case http.MethodPost:
		var body importCABody
		if !decodeBody(w, r, &body) {
			return
		}
		record, err := h.trustAnchorManager.ImportCA(r.Context(), body.Label, body.PEM)
		if err != nil {
			writeCAError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, record)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handlePlatformCAStatus enables or disables one trust anchor.
func (h *Hub) handlePlatformCAStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body caStatusBody
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.trustAnchorManager.SetCAStatus(r.Context(),
		r.PathValue("ca"), body.Status); err != nil {
		writeCAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePlatformCADelete removes one non-builtin trust anchor.
func (h *Hub) handlePlatformCADelete(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}
	if err := h.trustAnchorManager.DeleteCA(r.Context(), r.PathValue("ca")); err != nil {
		writeCAError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicCABundle returns the PEM bundle of every active CA with
// no authentication, for device provisioning. It is served from the
// gateway mux; the explicit path wins over the /v1/me/ ingest prefix.
func (h *Hub) handlePublicCABundle(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	bundle, err := h.trustAnchorManager.ActiveBundle(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "CA_BUNDLE_FAILED", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(bundle))
}
