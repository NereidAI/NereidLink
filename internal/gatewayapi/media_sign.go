// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// media_sign.go resolves a device media blob reference into a short-lived
// signed download URL for console users.

package gatewayapi

import (
	"net/http"

	"github.com/nereidai/nereidlink/internal/blob"
)

// signedBlobResponse carries one resolved download URL.
type signedBlobResponse struct {
	// URL is the short-lived presigned URL for the object.
	URL string `json:"url"`
	// Size is the object byte count.
	Size int64 `json:"size"`
	// ContentType is the declared or detected media type.
	ContentType string `json:"content_type"`
}

// handleSignMediaBlob signs a GET for a media object referenced by a
// blob:// telemetry value. The object key must be a well-formed media key
// scoped to the tenant in the path, so the device:read permission cannot
// be turned into access to firmware or another tenant's objects.
func (h *Hub) handleSignMediaBlob(w http.ResponseWriter, r *http.Request) {
	if h.blobs == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "BLOB_UNAVAILABLE",
			"blob storage is not configured")
		return
	}
	tenantID := r.PathValue("tenant")
	objectKey := r.URL.Query().Get("key")
	keyTenant, _, ok := blob.MediaKeyOwner(objectKey)
	if !ok || keyTenant != tenantID {
		writeRESTError(w, http.StatusBadRequest, "INVALID_BLOB_KEY",
			"key is not a media object in this tenant")
		return
	}
	size, contentType, err := h.blobs.ObjectMeta(r.Context(), objectKey)
	if err != nil {
		if err == blob.ErrObjectNotFound {
			writeRESTError(w, http.StatusNotFound, "BLOB_NOT_FOUND",
				"object no longer exists")
			return
		}
		writeRESTError(w, http.StatusInternalServerError, "BLOB_META_FAILED",
			"cannot read object metadata")
		return
	}
	urlStr, err := h.blobs.PresignGet(r.Context(), objectKey)
	if err != nil {
		if err == blob.ErrObjectNotFound {
			writeRESTError(w, http.StatusNotFound, "BLOB_NOT_FOUND",
				"object no longer exists")
			return
		}
		writeRESTError(w, http.StatusInternalServerError, "BLOB_SIGN_FAILED",
			"cannot sign object URL")
		return
	}
	writeRESTJSON(w, http.StatusOK, signedBlobResponse{
		URL:         urlStr,
		Size:        size,
		ContentType: contentType,
	})
}
