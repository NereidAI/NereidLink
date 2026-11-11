// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// local_blob_handler.go serves objects of the directory blob backend on
// HMAC-signed URLs. The signature in the query string is the only
// credential; requests are exempt from the bearer-token middleware.

package gatewayapi

import (
	"net/http"
	"strconv"

	"github.com/nereidai/nereidlink/internal/blob"
)

// handleGetLocalBlob authenticates the signed URL and streams the object
// through http.ServeContent, which implements Range, conditional requests
// and content negotiation so media players can seek without proxying the
// bytes anywhere else.
func (h *Hub) handleGetLocalBlob(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	objectKey := q.Get("key")
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || objectKey == "" {
		writeRESTError(w, http.StatusBadRequest, "INVALID_BLOB_URL",
			"malformed blob download URL")
		return
	}
	if !blob.VerifyObjectURL(h.blobSignKey, objectKey, exp, q.Get("sig")) {
		writeRESTError(w, http.StatusForbidden, "BLOB_URL_INVALID",
			"blob download URL expired or invalid")
		return
	}
	content, contentType, modTime, err := h.blobs.OpenReadSeeker(r.Context(), objectKey)
	if err != nil {
		if err == blob.ErrObjectNotFound {
			writeRESTError(w, http.StatusNotFound, "BLOB_NOT_FOUND",
				"object no longer exists")
			return
		}
		writeRESTError(w, http.StatusInternalServerError, "BLOB_READ_FAILED",
			"cannot read object")
		return
	}
	defer func() { _ = content.Close() }()
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, "", modTime, content)
}
