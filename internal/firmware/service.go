// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates the firmware repository: it streams uploaded
// bytes into object storage while hashing them, records metadata, enforces
// the per-tenant size cap and compatibility binding, and cleans up the blob
// when metadata cannot be committed. Bytes never pass through PostgreSQL.

package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/blob"
)

// defaultMaxUploadBytes caps a firmware upload unless overridden. It matches
// the blob section default so a misconfigured limit does not allow unbounded
// uploads.
const defaultMaxUploadBytes = 512 * 1024 * 1024

// Blobs is the narrow object-storage port the service needs. *blob.Store
// satisfies it; defining it here keeps this package free of S3 client types.
type Blobs interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
	DeleteObject(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string) (string, error)
}

// UploadInput is the validated upload metadata.
type UploadInput struct {
	// ProfileID optionally binds the package to a device model.
	ProfileID string
	// Name is the human-readable release name (required).
	Name string
	// ReleaseNotes optionally describes the release.
	ReleaseNotes string
	// Version is the target firmware version label (required).
	Version string
	// Filename is the original file name (required).
	Filename string
	// ContentType defaults to application/octet-stream when empty.
	ContentType string
	// IsStable marks the recommended rollback target for its model.
	IsStable bool
}

// Service implements repository use cases over a Store and Blobs.
type Service struct {
	store    *Store
	blobs    Blobs
	maxBytes int64
}

// NewService constructs a firmware service. A non-positive maxBytes falls
// back to the default cap.
func NewService(store *Store, blobs Blobs, maxBytes int64) *Service {
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}
	return &Service{store: store, blobs: blobs, maxBytes: maxBytes}
}

// MaxBytes returns the accepted upload size cap, so the HTTP layer can bound
// the request body before streaming.
func (s *Service) MaxBytes() int64 { return s.maxBytes }

// countReader counts bytes consumed so the size cap can be enforced even
// though the upload size is unknown up front.
type countReader struct {
	r io.Reader
	n *int64
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// Upload streams one package to object storage (hashing as it goes), then
// commits the metadata row. It returns ErrTooLarge if the stream exceeds the
// cap and ErrDuplicate for an already stored (tenant, profile, version).
func (s *Service) Upload(ctx context.Context, tenantID, createdBy string, input UploadInput, body io.Reader) (Package, error) {
	version := strings.TrimSpace(input.Version)
	filename := strings.TrimSpace(input.Filename)
	name := strings.TrimSpace(input.Name)
	if name == "" || version == "" || filename == "" {
		return Package{}, fmt.Errorf("%w: name, version and filename are required", ErrInvalidArgument)
	}
	contentType := strings.TrimSpace(input.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	profileID := strings.TrimSpace(input.ProfileID)
	if profileID != "" {
		ok, err := s.store.ProfileBelongsToTenant(ctx, tenantID, profileID)
		if err != nil {
			return Package{}, err
		}
		if !ok {
			return Package{}, fmt.Errorf("%w: profile does not belong to the tenant", ErrInvalidArgument)
		}
	}

	// A non-guessable content key; the sha256 is integrity metadata and also
	// surfaced for compatibility/selection.
	objectKey := blob.FirmwareKey(tenantID, uuid.NewString())

	hasher := sha256.New()
	// Allow one extra byte so a stream longer than the cap is detectable.
	var read int64
	stream := countReader{r: io.TeeReader(io.LimitReader(body, s.maxBytes+1), hasher), n: &read}

	size, err := s.blobs.PutObject(ctx, objectKey, stream, -1, contentType)
	if err != nil {
		return Package{}, fmt.Errorf("upload firmware blob: %w", err)
	}
	if read > s.maxBytes || size > s.maxBytes {
		_ = s.blobs.DeleteObject(ctx, objectKey)
		return Package{}, ErrTooLarge
	}
	if read == 0 {
		_ = s.blobs.DeleteObject(ctx, objectKey)
		return Package{}, fmt.Errorf("%w: empty package", ErrInvalidArgument)
	}

	pkg, err := s.store.Create(ctx, Package{
		TenantID:     tenantID,
		ProfileID:    profileID,
		Name:         name,
		ReleaseNotes: strings.TrimSpace(input.ReleaseNotes),
		Version:      version,
		Filename:     filename,
		ContentType:  contentType,
		ObjectKey:    objectKey,
		SizeBytes:    size,
		SHA256:       hex.EncodeToString(hasher.Sum(nil)),
		IsStable:     input.IsStable,
		CreatedBy:    createdBy,
	})
	if err != nil {
		// Do not orphan the uploaded bytes if metadata cannot be committed.
		_ = s.blobs.DeleteObject(ctx, objectKey)
		return Package{}, err
	}
	return pkg, nil
}

// List returns a filtered page of packages.
func (s *Service) List(ctx context.Context, tenantID string, filter Filter) (apipage.Page[Package], error) {
	return s.store.List(ctx, tenantID, filter)
}

// Get returns one package.
func (s *Service) Get(ctx context.Context, tenantID, id string) (Package, error) {
	return s.store.Get(ctx, tenantID, id)
}

// SetStable flips the stable marker.
func (s *Service) SetStable(ctx context.Context, tenantID, id string, stable bool) (Package, error) {
	return s.store.SetStable(ctx, tenantID, id, stable)
}

// Delete removes metadata and the backing object. The blob delete is best
// effort: metadata is the source of truth for reachability.
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	pkg, err := s.store.Delete(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if err := s.blobs.DeleteObject(ctx, pkg.ObjectKey); err != nil {
		return fmt.Errorf("delete firmware blob: %w", err)
	}
	return nil
}

// PresignDownload returns a short-lived admin/browser download URL for a
// package. Device downloads do not use this path (they go through the
// authenticated /v1/me relay), but operators may fetch from the repository UI.
func (s *Service) PresignDownload(ctx context.Context, tenantID, id string) (string, Package, error) {
	pkg, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return "", Package{}, err
	}
	url, err := s.blobs.PresignGet(ctx, pkg.ObjectKey)
	if err != nil {
		return "", Package{}, fmt.Errorf("presign firmware: %w", err)
	}
	return url, pkg, nil
}
