// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// command_catalog_handlers.go exposes the per-device command whitelist
// (command catalog) management API. Reads need device:read, mutations
// device:write; dispatching a command remains device:command.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/command"
)

// CommandCatalogAdmin is the management surface the handlers need.
type CommandCatalogAdmin interface {
	List(ctx context.Context, tenantID, deviceID string) ([]command.CatalogEntryView, error)
	Create(ctx context.Context, tenantID, deviceID string, in command.CatalogEntryInput) (command.CatalogEntryView, error)
	Update(ctx context.Context, tenantID, deviceID, name string, in command.CatalogEntryInput) (command.CatalogEntryView, error)
	Delete(ctx context.Context, tenantID, deviceID, name string) error
}

// writeCatalogError maps command-package errors to REST statuses.
func writeCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, command.ErrInvalidCatalogEntry):
		writeRESTError(w, http.StatusBadRequest, "COMMAND_CATALOG_INVALID", err.Error())
	case errors.Is(err, command.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "COMMAND_CATALOG_NOT_FOUND",
			"command is not declared in the catalog")
	default:
		writeRESTError(w, http.StatusInternalServerError, "COMMAND_CATALOG_FAILED", err.Error())
	}
}

// handleListCommandCatalog returns every declared command of the device.
func (h *Hub) handleListCommandCatalog(w http.ResponseWriter, r *http.Request) {
	if h.commandCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command service is not enabled")
		return
	}
	entries, err := h.commandCatalog.List(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"))
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	// Arrays are always emitted as [] rather than null.
	if entries == nil {
		entries = []command.CatalogEntryView{}
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": entries})
}

// handleCreateCommandCatalog declares one command.
func (h *Hub) handleCreateCommandCatalog(w http.ResponseWriter, r *http.Request) {
	if h.commandCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command service is not enabled")
		return
	}
	var body command.CatalogEntryInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	entry, err := h.commandCatalog.Create(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), body)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, entry)
}

// handleUpdateCommandCatalog replaces one declaration named in the path.
func (h *Hub) handleUpdateCommandCatalog(w http.ResponseWriter, r *http.Request) {
	if h.commandCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command service is not enabled")
		return
	}
	var body command.CatalogEntryInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return
	}
	name := r.PathValue("command")
	body.Name = name
	entry, err := h.commandCatalog.Update(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), name, body)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, entry)
}

// handleDeleteCommandCatalog removes one declaration.
func (h *Hub) handleDeleteCommandCatalog(w http.ResponseWriter, r *http.Request) {
	if h.commandCatalog == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "COMMAND_SERVICE_UNAVAILABLE",
			"command service is not enabled")
		return
	}
	err := h.commandCatalog.Delete(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		strings.TrimSpace(r.PathValue("command")))
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
