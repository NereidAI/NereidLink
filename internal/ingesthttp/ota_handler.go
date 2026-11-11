// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ota_handler.go exposes firmware OTA to directly connected devices over the
// authenticated device HTTPS ingress (ADR-0006b): a device polls its open
// assignments, downloads the platform-hosted package through an authorized
// streaming relay that supports HTTP Range (resume), and reports per-device
// progress. The same download endpoint authorizes a gateway fetching on
// behalf of its attached sub-devices. Bytes are streamed from object storage
// and never buffered whole.

package ingesthttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nereidai/nereidlink/internal/ota"
)

// OTADeviceService is the device-facing OTA surface. *ota.Manager satisfies it.
type OTADeviceService interface {
	OpenAssignments(ctx context.Context, tenantID, deviceID string) ([]ota.DirectAssignment, error)
	AuthorizeDownload(ctx context.Context, tenantID, callerID, taskID string) (ota.DownloadGrant, error)
	ApplyDeviceProgress(ctx context.Context, ev ota.Event) (ota.Task, bool, error)
}

// BlobStreamer streams a possibly ranged object from storage. *blob.Store
// satisfies it.
type BlobStreamer interface {
	GetObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// SetOTA wires device firmware OTA into the HTTP ingress.
func (s *Server) SetOTA(svc OTADeviceService, blobs BlobStreamer) {
	s.ota = svc
	s.otaBlobs = blobs
}

// progressBody is the device-reported rollout progress.
type progressBody struct {
	State       string `json:"state"`
	ProgressPct int    `json:"progress_pct"`
	Message     string `json:"message"`
	// ResultCode is the structured failure reason (ota.Result* codes).
	ResultCode string `json:"result_code,omitempty"`
	// Bytes is the number of package bytes received so far.
	Bytes int64 `json:"bytes,omitempty"`
	// FirmwareVersion carries the running version on the post-reboot
	// "confirmed" frame under the confirmed_after_reboot policy; on a plain
	// success the manager writes the task version regardless.
	FirmwareVersion string `json:"firmware_version,omitempty"`
}

// handleOTAList returns the caller's open, platform-hosted firmware jobs.
func (s *Server) handleOTAList(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid access token")
		return
	}
	if s.ota == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "OTA_UNAVAILABLE", "ota is not enabled")
		return
	}
	jobs, err := s.ota.OpenAssignments(r.Context(), identity.TenantID, identity.DeviceID)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "OTA_LIST_FAILED", "failed to list firmware jobs")
		return
	}
	writeDeviceJSON(w, http.StatusOK, map[string]any{"assignments": jobs})
}

// handleOTAProgress records one device progress frame.
func (s *Server) handleOTAProgress(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid access token")
		return
	}
	if s.ota == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "OTA_UNAVAILABLE", "ota is not enabled")
		return
	}
	var body progressBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeDeviceError(w, http.StatusBadRequest, "MALFORMED_BODY", "invalid progress json")
		return
	}
	state := normalizeDeviceOTAState(body.State)
	if state == "" {
		writeDeviceError(w, http.StatusBadRequest, "OTA_INVALID_STATE", "unknown progress state")
		return
	}
	if body.ResultCode != "" && !ota.ValidResultCode(body.ResultCode) {
		writeDeviceError(w, http.StatusBadRequest, "OTA_INVALID_RESULT_CODE", "unknown result code")
		return
	}
	_, _, err := s.ota.ApplyDeviceProgress(r.Context(), ota.Event{
		TaskID:          r.PathValue("task"),
		TenantID:        identity.TenantID,
		DeviceID:        identity.DeviceID,
		State:           state,
		ResultCode:      body.ResultCode,
		ProgressPct:     body.ProgressPct,
		Bytes:           body.Bytes,
		FirmwareVersion: body.FirmwareVersion,
		Message:         body.Message,
		Source:          ota.SourceDevice,
	})
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "OTA_PROGRESS_FAILED", "failed to record progress")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeDeviceOTAState maps a device-reported state (lower-case or the
// documented upper-case edge spelling, plus SUCCESS) to the internal phase.
func normalizeDeviceOTAState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ota.ItemStatePending, ota.ItemStateInitiated, ota.ItemStateDownloading,
		ota.ItemStateDownloaded, ota.ItemStateVerified, ota.ItemStateUpdating,
		ota.ItemStateUpdated, ota.ItemStateRebooting, ota.ItemStateConfirmed,
		ota.ItemStateFailed:
		return strings.ToLower(strings.TrimSpace(s))
	case "success", "succeeded":
		return ota.ItemStateSucceeded
	default:
		return ""
	}
}

// handleOTAPackage streams the authorized firmware bytes, honoring a single
// byte Range for resume. Authorization is per task member (the device itself
// or its gateway) and re-checked on every request.
func (s *Server) handleOTAPackage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid access token")
		return
	}
	if s.ota == nil || s.otaBlobs == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "OTA_UNAVAILABLE", "firmware hosting is not enabled")
		return
	}
	taskID := r.PathValue("task")
	grant, err := s.ota.AuthorizeDownload(r.Context(), identity.TenantID, identity.DeviceID, taskID)
	if err != nil {
		writeOTADownloadError(w, err)
		return
	}

	offset, length, ranged, ok := parseSingleRange(r.Header.Get("Range"), grant.Size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", grant.Size))
		writeDeviceError(w, http.StatusRequestedRangeNotSatisfiable, "BAD_RANGE", "unsupported range")
		return
	}
	if ranged && length == 0 && grant.Size > 0 {
		// Open-ended "bytes=N-": length 0 signals open-ended to storage, but
		// the HTTP response needs the concrete remaining byte count.
		length = grant.Size - offset
	}

	stream, err := s.otaBlobs.GetObjectRange(r.Context(), grant.ObjectKey, offset, length)
	if err != nil {
		writeDeviceError(w, http.StatusBadGateway, "OTA_FETCH_FAILED", "cannot open firmware object")
		return
	}
	// Streaming happens after headers are sent; a close error on the already
	// consumed body cannot be surfaced to the client, so it is discarded.
	defer func() { _ = stream.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Firmware-Sha256", grant.SHA256)
	if ranged {
		end := offset + length - 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, grant.Size))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(grant.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	// Flush status/headers before streaming the body.
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if _, err := io.Copy(w, stream); err != nil && !errors.Is(r.Context().Err(), context.Canceled) {
		// Headers are already sent; only abort the connection on a real
		// upstream failure.
		return
	}
}

// parseSingleRange parses one "bytes=start-end" range. It returns the minio
// length argument (0 = open-ended). Multipart ranges are rejected.
func parseSingleRange(header string, size int64) (offset, length int64, ranged, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, 0, false, true
	}
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	startText, endText, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false, false
	}
	switch {
	case startText == "":
		// Suffix range: bytes=-N (last N bytes).
		n, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || n <= 0 || size <= 0 {
			return 0, 0, false, false
		}
		if n > size {
			n = size
		}
		return size - n, n, true, true
	case endText == "":
		start, err := strconv.ParseInt(startText, 10, 64)
		if err != nil || start < 0 || (size > 0 && start >= size) {
			return 0, 0, false, false
		}
		return start, 0, true, true
	default:
		start, err := strconv.ParseInt(startText, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, false, false
		}
		end, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return 0, 0, false, false
		}
		if size > 0 && start >= size {
			return 0, 0, false, false
		}
		if size > 0 && end >= size {
			end = size - 1
		}
		return start, end - start + 1, true, true
	}
}

// writeOTADownloadError maps OTA authorization errors to status codes.
func writeOTADownloadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ota.ErrNotFound):
		writeDeviceError(w, http.StatusNotFound, "OTA_NOT_FOUND", "firmware task not found")
	case errors.Is(err, ota.ErrNotTarget):
		writeDeviceError(w, http.StatusForbidden, "OTA_FORBIDDEN", "caller is not a task target")
	case errors.Is(err, ota.ErrNotActive):
		writeDeviceError(w, http.StatusForbidden, "OTA_NOT_ACTIVE", "firmware task is not active")
	case errors.Is(err, ota.ErrNotHosted):
		writeDeviceError(w, http.StatusConflict, "OTA_NOT_HOSTED", "task uses an external package url")
	default:
		writeDeviceError(w, http.StatusInternalServerError, "OTA_AUTH_FAILED", "download authorization failed")
	}
}
