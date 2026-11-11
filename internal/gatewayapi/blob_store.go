// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// blob_store.go declares the object-storage port the REST layer needs
// (ADR-0006). The concrete implementation lives in internal/blob; keeping the
// interface here avoids gatewayapi importing the S3 client and lets the
// firmware handlers be nil-safe when object storage is not configured.

package gatewayapi

import (
	"context"
	"io"
	"time"
)

// BlobStore stores platform-hosted binary objects and issues presigned
// download URLs. Firmware package upload handlers depend on it.
type BlobStore interface {
	// PutObject uploads one object and returns its stored size.
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
	// StatObject returns the size and ETag of an object.
	StatObject(ctx context.Context, key string) (size int64, etag string, err error)
	// ObjectMeta returns the size and content type of an object.
	ObjectMeta(ctx context.Context, key string) (size int64, contentType string, err error)
	// GetObject streams an object; the caller closes the reader.
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	// OpenReadSeeker opens an object for HTTP Range serving. The return
	// type is io.ReadSeekCloser, kept anonymous so both blob backends
	// satisfy the interface without importing gatewayapi.
	OpenReadSeeker(ctx context.Context, key string) (io.ReadSeekCloser, string, time.Time, error)
	// DeleteObject removes an object (idempotent).
	DeleteObject(ctx context.Context, key string) error
	// PresignGet returns a short-lived presigned GET URL.
	PresignGet(ctx context.Context, key string) (string, error)
	// Bucket returns the configured bucket name.
	Bucket() string
}

// SetBlobStore wires object storage for platform-hosted binaries.
func (h *Hub) SetBlobStore(store BlobStore) {
	h.blobs = store
}

// SetBlobSigningKey wires the HMAC key for local-backend download URLs.
func (h *Hub) SetBlobSigningKey(key string) {
	h.blobSignKey = key
}
