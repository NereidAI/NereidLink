// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_handlers.go exposes device shadow listing and platform-writable
// attribute upsert for the device detail page.

package gatewayapi

import (
	"encoding/json"
	"net/http"
)

// attributeUpsertBody is the PUT document for one attribute.
type attributeUpsertBody struct {
	// Scope is SERVER_SCOPE or SHARED_SCOPE.
	Scope string `json:"scope"`

	// Key is the attribute key.
	Key string `json:"key"`

	// Value is the raw JSON value.
	Value json.RawMessage `json:"value"`
}

// handleListAttributes returns attributes of a device, optionally filtered
// by the scope query parameter.
func (h *Hub) handleListAttributes(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	attributes, err := h.registryAdmin.ListAttributes(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), r.URL.Query().Get("scope"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, attributes)
}

// handlePutAttribute upserts one platform-writable attribute.
func (h *Hub) handlePutAttribute(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	var body attributeUpsertBody
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.registryAdmin.PutAttribute(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		body.Scope, body.Key, body.Value); err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAttribute deletes one platform-writable attribute
// (SERVER_SCOPE/SHARED_SCOPE) identified by the scope and key query
// parameters. The deletion is fanned out to a live device as a null value.
func (h *Hub) handleDeleteAttribute(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	scope := r.URL.Query().Get("scope")
	key := r.URL.Query().Get("key")
	if key == "" {
		writeRESTError(w, http.StatusBadRequest, "ATTRIBUTE_KEY_REQUIRED", "key query parameter is required")
		return
	}
	if err := h.registryAdmin.DeleteAttribute(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), scope, key); err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
