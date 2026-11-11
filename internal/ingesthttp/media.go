// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// media.go defines the blob port and device-facing media endpoints:
// binary upload (POST /v1/me/blob) and ranged download (GET /v1/me/blob).

package ingesthttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/nereidai/nereidlink/internal/blob"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// MediaBlobs is the blob backend port for device media. Both blob backends
// satisfy it through the shared interface.
type MediaBlobs interface {
	// PutObject streams one media object into storage.
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
	// OpenReadSeeker opens an object for ranged HTTP serving.
	OpenReadSeeker(ctx context.Context, key string) (io.ReadSeekCloser, string, time.Time, error)
	// DeleteObject removes an object if the associated publish fails.
	DeleteObject(ctx context.Context, key string) error
}

// SetMediaBlobs wires the blob backend and the shared media policy after
// construction. mediaStore offloads frame-marker fields; maxBytes bounds
// direct binary uploads.
func (s *Server) SetMediaBlobs(blobs MediaBlobs, mediaStore sdktelemetry.MediaStore, maxBytes int64) {
	s.mediaBlobs = blobs
	s.mediaStore = mediaStore
	if s.tune.MaxMediaBytes == 0 {
		s.tune.MaxMediaBytes = maxBytes
	}
}

// measurementKeyPattern accepts telemetry measurement identifiers:
// letters, digits, underscore, dot and dash, so a device cannot smuggle
// arbitrary JSON through the synthesized telemetry frame.
var measurementKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// blobUploadResponse reports the stored media and its reference.
type blobUploadResponse struct {
	// Published is the number of telemetry points produced (always 1).
	Published int `json:"published"`
	// Key is the blob object key.
	Key string `json:"key"`
	// URI is the telemetry value stored (blob://<key>).
	URI string `json:"uri"`
	// Size is the stored byte count.
	Size int64 `json:"size"`
}

// handleBlobUpload streams the request body straight into blob storage,
// then publishes one telemetry point referencing it. The measurement key
// is the "key" query parameter (default "blob"); the stored object key
// is generated server side and never accepted from the client.
func (s *Server) handleBlobUpload(w http.ResponseWriter, r *http.Request) {
	if s.mediaBlobs == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "BLOB_UNAVAILABLE",
			"blob storage is not configured")
		return
	}
	measurementKey := r.URL.Query().Get("key")
	if measurementKey == "" {
		measurementKey = "blob"
	}
	if !measurementKeyPattern.MatchString(measurementKey) {
		writeDeviceError(w, http.StatusBadRequest, "INVALID_MEASUREMENT_KEY",
			"measurement key may contain only letters, digits, '_', '.' and '-'")
		return
	}
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	// Bound the body before streaming; MaxBytesReader surfaces the limit
	// while PutObject consumes the reader so bytes never buffer wholesale.
	contentType := r.Header.Get("Content-Type")
	if policy, ok := s.mediaStore.(interface {
		ContentTypeAllowed(string) bool
	}); ok && !policy.ContentTypeAllowed(contentType) {
		writeDeviceError(w, http.StatusUnsupportedMediaType, "BLOB_TYPE_REJECTED",
			"content type is not accepted for media")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.tune.MaxMediaBytes)
	objectKey := blob.MediaKey(identity.TenantID, identity.DeviceID, uuid.NewString())
	size, putErr := s.mediaBlobs.PutObject(r.Context(), objectKey, r.Body, -1, contentType)
	if putErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(putErr, &maxErr) {
			writeDeviceError(w, http.StatusRequestEntityTooLarge, "BLOB_TOO_LARGE",
				"media object exceeds the accepted size")
			return
		}
		writeDeviceError(w, http.StatusBadGateway, "BLOB_STORE_FAILED",
			"failed to store media object")
		return
	}

	// Reuse the normal telemetry path with a synthesized frame so the blob
	// reference goes through descriptor handling, accounting and limits
	// exactly like any other point.
	frame, _ := json.Marshal(map[string]string{measurementKey: sdktelemetry.BlobURIScheme + objectKey})
	published, pubErr := sdktelemetry.Publish(r.Context(), s.js, s.resolver,
		sdktelemetry.Identity{
			TenantID: identity.TenantID,
			DeviceID: identity.DeviceID,
		}, "HTTP", frame, time.Now(),
		sdktelemetry.WithEncoder(s.encoder),
		sdktelemetry.WithMediaStore(s.mediaStore))
	if pubErr != nil {
		// Do not orphan the object when its telemetry point could not be
		// published.
		_ = s.mediaBlobs.DeleteObject(r.Context(), objectKey)
		writeDeviceError(w, http.StatusBadGateway, "BLOB_PUBLISH_FAILED",
			"stored media but failed to publish its telemetry reference")
		return
	}
	writeDeviceJSON(w, http.StatusAccepted, blobUploadResponse{
		Published: published,
		Key:       objectKey,
		URI:       sdktelemetry.BlobURIScheme + objectKey,
		Size:      size,
	})
}

// handleBlobDownload serves one media object owned by the authenticated
// device. http.ServeContent provides Range, ETag and conditional requests.
func (s *Server) handleBlobDownload(w http.ResponseWriter, r *http.Request) {
	if s.mediaBlobs == nil {
		writeDeviceError(w, http.StatusServiceUnavailable, "BLOB_UNAVAILABLE",
			"blob storage is not configured")
		return
	}
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	objectKey := r.URL.Query().Get("key")
	tenantID, deviceID, valid := blob.MediaKeyOwner(objectKey)
	if !valid || tenantID != identity.TenantID || deviceID != identity.DeviceID {
		writeDeviceError(w, http.StatusForbidden, "BLOB_ACCESS_DENIED",
			"object is not owned by this device")
		return
	}
	content, contentType, modTime, err := s.mediaBlobs.OpenReadSeeker(r.Context(), objectKey)
	if err != nil {
		if errors.Is(err, blob.ErrObjectNotFound) {
			writeDeviceError(w, http.StatusNotFound, "BLOB_NOT_FOUND",
				"object no longer exists")
			return
		}
		writeDeviceError(w, http.StatusBadGateway, "BLOB_READ_FAILED",
			"cannot read object")
		return
	}
	defer func() { _ = content.Close() }()
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, "", modTime, content)
}
