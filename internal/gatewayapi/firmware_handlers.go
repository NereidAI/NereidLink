// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// firmware_handlers.go exposes the platform-hosted firmware repository
// (ADR-0006b): multipart upload that streams the artifact straight into object
// storage while hashing it, filtered paged listing, per-package get/delete,
// stable marking and an operator presigned download. Device package downloads
// are separate and authenticated through the /v1/me relay.

package gatewayapi

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/firmware"
)

// FirmwareService is the firmware repository surface.
type FirmwareService interface {
	Upload(ctx context.Context, tenantID, createdBy string, input firmware.UploadInput, body io.Reader) (firmware.Package, error)
	List(ctx context.Context, tenantID string, filter firmware.Filter) (apipage.Page[firmware.Package], error)
	Get(ctx context.Context, tenantID, id string) (firmware.Package, error)
	SetStable(ctx context.Context, tenantID, id string, stable bool) (firmware.Package, error)
	Delete(ctx context.Context, tenantID, id string) error
	PresignDownload(ctx context.Context, tenantID, id string) (string, firmware.Package, error)
	MaxBytes() int64
}

// SetFirmware wires the firmware repository service.
func (h *Hub) SetFirmware(service FirmwareService) {
	h.firmware = service
}

// handleFirmware lists packages or accepts a streamed multipart upload.
func (h *Hub) handleFirmware(w http.ResponseWriter, r *http.Request) {
	if h.firmware == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "FIRMWARE_UNAVAILABLE",
			"firmware repository is not enabled (object storage not configured)")
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		h.listFirmware(w, r, tenant)
	case http.MethodPost:
		h.uploadFirmware(w, r, tenant)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// listFirmware returns a filtered, paged package list.
func (h *Hub) listFirmware(w http.ResponseWriter, r *http.Request, tenant string) {
	page, pageSize := parsePagination(r)
	q := r.URL.Query()
	result, err := h.firmware.List(r.Context(), tenant, firmware.Filter{
		ProfileID: strings.TrimSpace(q.Get("profile_id")),
		Search:    strings.TrimSpace(q.Get("search")),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "FIRMWARE_INVALID", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// uploadFirmware streams one artifact. Form metadata fields are read first,
// then the file part is piped to object storage without buffering the whole
// artifact in memory or on disk.
func (h *Hub) uploadFirmware(w http.ResponseWriter, r *http.Request, tenant string) {
	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		writeRESTError(w, http.StatusUnsupportedMediaType, "FIRMWARE_CONTENT_TYPE",
			"expected multipart/form-data")
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "FIRMWARE_MALFORMED", "malformed multipart body")
		return
	}

	input := firmware.UploadInput{}
	var file io.Reader
	var filePart *multipart.Part
	var partFilename string
	// Parts are streamed: metadata text fields must precede the file part
	// (the frontend appends fields first). Once the file part is reached we
	// stop iterating, because multipart.Reader.NextPart discards the
	// unread remainder of the current part, and pipe that reader straight to
	// object storage without buffering the artifact.
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "FIRMWARE_MALFORMED", "malformed multipart part")
			return
		}
		if part.FormName() == "file" {
			file = part
			filePart = part
			partFilename = part.FileName()
			break
		}
		fieldCap := int64(4096)
		if part.FormName() == "release_notes" {
			fieldCap = 64 * 1024
		}
		value, err := io.ReadAll(io.LimitReader(part, fieldCap))
		if closeErr := part.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "FIRMWARE_MALFORMED", "cannot read form field")
			return
		}
		switch part.FormName() {
		case "name":
			input.Name = strings.TrimSpace(string(value))
		case "release_notes":
			input.ReleaseNotes = strings.TrimSpace(string(value))
		case "version":
			input.Version = strings.TrimSpace(string(value))
		case "filename":
			input.Filename = strings.TrimSpace(string(value))
		case "profile_id":
			input.ProfileID = strings.TrimSpace(string(value))
		case "content_type":
			input.ContentType = strings.TrimSpace(string(value))
		case "is_stable":
			input.IsStable = string(value) == "true" || string(value) == "1"
		}
	}

	if file == nil {
		writeRESTError(w, http.StatusBadRequest, "FIRMWARE_NO_FILE", "missing file part")
		return
	}
	if input.Filename == "" {
		input.Filename = partFilename
	}

	// Bound the body one byte over the cap so oversize uploads fail while
	// streaming rather than after committing metadata.
	limited := io.LimitReader(file, h.firmware.MaxBytes()+1)
	pkg, err := h.firmware.Upload(r.Context(), tenant, otaActor(r), input, limited)
	// The upload has already committed (or failed); closing the request part
	// cannot change that outcome, so its error is intentionally discarded.
	_ = filePart.Close()
	if err != nil {
		h.writeFirmwareError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, pkg)
}

// handleFirmwareOne serves get/delete for one package.
func (h *Hub) handleFirmwareOne(w http.ResponseWriter, r *http.Request) {
	if h.firmware == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "FIRMWARE_UNAVAILABLE",
			"firmware repository is not enabled")
		return
	}
	tenant, id := r.PathValue("tenant"), r.PathValue("firmware")
	switch r.Method {
	case http.MethodGet:
		pkg, err := h.firmware.Get(r.Context(), tenant, id)
		if err != nil {
			h.writeFirmwareError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, pkg)
	case http.MethodDelete:
		if err := h.firmware.Delete(r.Context(), tenant, id); err != nil {
			h.writeFirmwareError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleFirmwareStable marks/unmarks a recommended rollback version.
func (h *Hub) handleFirmwareStable(w http.ResponseWriter, r *http.Request) {
	if h.firmware == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "FIRMWARE_UNAVAILABLE",
			"firmware repository is not enabled")
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	stable := r.URL.Query().Get("stable") != "false"
	pkg, err := h.firmware.SetStable(r.Context(), r.PathValue("tenant"),
		r.PathValue("firmware"), stable)
	if err != nil {
		h.writeFirmwareError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, pkg)
}

// handleFirmwareDownload returns an operator presigned URL for browser use.
func (h *Hub) handleFirmwareDownload(w http.ResponseWriter, r *http.Request) {
	if h.firmware == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "FIRMWARE_UNAVAILABLE",
			"firmware repository is not enabled")
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	url, pkg, err := h.firmware.PresignDownload(r.Context(),
		r.PathValue("tenant"), r.PathValue("firmware"))
	if err != nil {
		h.writeFirmwareError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]string{
		"url":      url,
		"filename": pkg.Filename,
		"version":  pkg.Version,
	})
}

// writeFirmwareError maps firmware domain errors to REST status codes.
func (h *Hub) writeFirmwareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, firmware.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "FIRMWARE_NOT_FOUND", "firmware package not found")
	case errors.Is(err, firmware.ErrDuplicate):
		writeRESTError(w, http.StatusConflict, "FIRMWARE_DUPLICATE", err.Error())
	case errors.Is(err, firmware.ErrTooLarge):
		writeRESTError(w, http.StatusRequestEntityTooLarge, "FIRMWARE_TOO_LARGE", err.Error())
	case errors.Is(err, firmware.ErrInUse):
		writeRESTError(w, http.StatusConflict, "FIRMWARE_IN_USE", err.Error())
	case errors.Is(err, firmware.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "FIRMWARE_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "FIRMWARE_FAILED", err.Error())
	}
}
