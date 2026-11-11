// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package firmware manages the platform-hosted firmware repository
// (ADR-0006b). It stores only package metadata in PostgreSQL; the binary
// itself lives in the configured object store (internal/blob) and is streamed
// to devices through an authenticated relay. A package belongs to one tenant
// and is optionally bound to a device profile/model for compatibility checks;
// upgrade and rollback are the same operation (deploy any stored version), so
// no direction is modelled here.
package firmware

import (
	"errors"
	"time"
)

// Package is one stored firmware artifact's metadata.
type Package struct {
	// ID is the repository id.
	ID string `json:"id"`
	// TenantID scopes the package.
	TenantID string `json:"tenant_id"`
	// ProfileID optionally binds the package to one device model; empty
	// means profile-agnostic.
	ProfileID string `json:"profile_id,omitempty"`
	// Name is the human-readable release name shown in the console; it is
	// independent of both the device-facing Version and the Filename.
	Name string `json:"name"`
	// ReleaseNotes is optional free text describing the release.
	ReleaseNotes string `json:"release_notes,omitempty"`
	// Version is the firmware version label the device reports after apply.
	Version string `json:"version"`
	// Filename is the original uploaded file name (informational).
	Filename string `json:"filename"`
	// ContentType is the stored object MIME type.
	ContentType string `json:"content_type"`
	// ObjectKey is the blob key (not exposed as a public URL to devices).
	ObjectKey string `json:"-"`
	// SizeBytes is the exact artifact size.
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is the mandatory lowercase hex digest the edge enforces.
	SHA256 string `json:"sha256"`
	// IsStable marks a recommended rollback target.
	IsStable bool `json:"is_stable"`
	// CreatedBy records the uploading principal for audit.
	CreatedBy string `json:"created_by,omitempty"`
	// CreatedAt / UpdatedAt are repository timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Filter narrows a paged package listing.
type Filter struct {
	// ProfileID filters by model; the empty string returns all. The special
	// value "none" returns profile-agnostic packages.
	ProfileID string
	// Search matches name/version/filename case-insensitively as a substring.
	Search string
	// Page/PageSize are 1-based page and page length.
	Page     int
	PageSize int
}

// Sentinel errors mapped to REST status codes by the gateway.
var (
	// ErrNotFound means no such package for the tenant.
	ErrNotFound = errors.New("firmware package not found")
	// ErrInvalidArgument means the upload input failed validation.
	ErrInvalidArgument = errors.New("invalid firmware package")
	// ErrDuplicate means (tenant, profile, version) already exists.
	ErrDuplicate = errors.New("firmware package version already exists for this model")
	// ErrTooLarge means the upload exceeds the configured size cap.
	ErrTooLarge = errors.New("firmware package exceeds maximum upload size")
	// ErrInUse means the package is referenced by an OTA task and cannot be
	// deleted while that reference exists.
	ErrInUse = errors.New("firmware package is in use by an ota task")
)
