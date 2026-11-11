// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// media_store.go adapts a blob backend to the sdktelemetry.MediaStore port:
// frame fields marked as binary are decoded by the telemetry publisher and
// handed here for validation and storage under a generated media key.

package blob

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/google/uuid"

	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// MediaSink is the storage capability MediaStore needs; both blob backends
// satisfy it.
type MediaSink interface {
	// PutObject uploads one object and returns its stored size.
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
}

// Media policy defaults.
const (
	// defaultMediaMaxBytes caps one media object (10 MiB). Firmware
	// packages have a separate, much larger limit.
	defaultMediaMaxBytes = 10 * 1024 * 1024
	// defaultMediaTypePrefix accepts image media when no allowlist given.
	defaultMediaTypePrefix = "image/"
)

// Media validation errors. Ingresses map these to 413/415 responses.
var (
	// ErrMediaTooLarge reports an object over the configured byte limit.
	ErrMediaTooLarge = errors.New("media object too large")
	// ErrMediaTypeRejected reports a content type outside the allowlist.
	ErrMediaTypeRejected = errors.New("media content type not allowed")
)

// MediaStore implements sdktelemetry.MediaStore over a MediaSink. It is
// shared by every ingress (HTTP, MQTT, CoAP, templates) so size and type
// policy is enforced exactly once.
type MediaStore struct {
	sink            MediaSink
	maxBytes        int64
	allowedPrefixes []string
}

// NewMediaStore constructs the adapter. maxBytes <= 0 selects the default;
// an empty allowedTypes permits image/*. An entry ending in "/" matches a
// content-type prefix; other entries match exactly.
func NewMediaStore(sink MediaSink, maxBytes int64, allowedTypes []string) *MediaStore {
	if maxBytes <= 0 {
		maxBytes = defaultMediaMaxBytes
	}
	if len(allowedTypes) == 0 {
		allowedTypes = []string{defaultMediaTypePrefix}
	}
	return &MediaStore{sink: sink, maxBytes: maxBytes, allowedPrefixes: allowedTypes}
}

// MaxBytes returns the configured object size limit; HTTP ingresses use it
// for MaxBytesReader so oversized streams are cut off during upload.
func (m *MediaStore) MaxBytes() int64 { return m.maxBytes }

// ContentTypeAllowed reports whether contentType passes the allowlist;
// HTTP ingresses call it before streaming an upload so a rejected type is
// refused without reading the body.
func (m *MediaStore) ContentTypeAllowed(contentType string) bool {
	return m.contentTypeAllowed(contentType)
}

// PutMedia validates and stores one media object and returns its key. The
// key is always generated server side from tenant/device scopes; the
// filename parameter is intentionally ignored for key construction and
// only callers that persist descriptors use it.
func (m *MediaStore) PutMedia(ctx context.Context, id sdktelemetry.Identity,
	contentType, _ string, data []byte) (string, error) {
	if int64(len(data)) > m.maxBytes {
		return "", ErrMediaTooLarge
	}
	if !m.contentTypeAllowed(contentType) {
		return "", ErrMediaTypeRejected
	}
	objectKey := MediaKey(id.TenantID, id.DeviceID, uuid.NewString())
	if _, err := m.sink.PutObject(ctx, objectKey,
		bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		return "", err
	}
	return objectKey, nil
}

// contentTypeAllowed reports whether contentType matches the allowlist.
// An empty content type passes: the platform stores no type and lets the
// read side sniff, instead of guessing a media type.
func (m *MediaStore) contentTypeAllowed(contentType string) bool {
	if contentType == "" {
		return true
	}
	for _, allowed := range m.allowedPrefixes {
		if len(allowed) > 0 && allowed[len(allowed)-1] == '/' {
			if len(contentType) >= len(allowed) && contentType[:len(allowed)] == allowed {
				return true
			}
		} else if contentType == allowed {
			return true
		}
	}
	return false
}
