// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// license_handlers.go exposes license state, tenant suspension and usage
// facts. Deployment license installation is available both through the
// operations CLI and, for the platform super administrator, through the
// REST API; both paths verify the signed envelope with the same service.

package gatewayapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/license"
)

// LicenseAdmin is the license and usage surface.
type LicenseAdmin interface {
	// EnsurePlatformCanCreateTenant enforces deployment license gates.
	EnsurePlatformCanCreateTenant(ctx context.Context) error

	// DeploymentInfo returns the installed deployment license and gate state.
	DeploymentInfo(ctx context.Context) (license.DeploymentInfo, error)

	// InstallDeployment verifies and installs a deployment-scope license.
	InstallDeployment(ctx context.Context, file license.SignedFile, actor string) (license.Payload, error)

	// EnsureTenantCanCreateDevice enforces tenant device-cap gates.
	EnsureTenantCanCreateDevice(ctx context.Context, tenantID string) error

	// TenantState evaluates the effective tenant license.
	TenantState(ctx context.Context, tenantID string, now time.Time) (license.State, error)

	// SuspendTenant immediately suspends an active tenant.
	SuspendTenant(ctx context.Context, tenantID, reason, actor string) error

	// ResumeTenant restores a suspended tenant.
	ResumeTenant(ctx context.Context, tenantID, actor string) error

	// ListUsage returns usage facts across tenants.
	ListUsage(ctx context.Context, from, to time.Time) ([]license.UsageRow, error)

	// TenantUsage returns one tenant's usage facts.
	TenantUsage(ctx context.Context, tenantID string, from, to time.Time) ([]license.UsageRow, error)
}

// SetLicenseAdmin wires the license service.
func (h *Hub) SetLicenseAdmin(admin LicenseAdmin) {
	h.licenseAdmin = admin
}

type suspendTenantBody struct {
	Reason string `json:"reason"`
}

// maxLicenseUploadBytes bounds a license envelope upload. A signed envelope
// is only a few hundred bytes; the generous cap rejects accidental multi-MB
// bodies without buffering them.
const maxLicenseUploadBytes = 1 << 20

// handlePlatformLicense returns the deployment license status for the
// platform administrator console.
func (h *Hub) handlePlatformLicense(w http.ResponseWriter, r *http.Request) {
	info, err := h.licenseAdmin.DeploymentInfo(r.Context())
	if h.writeLicenseError(w, err) {
		return
	}
	if info.Features == nil {
		// API arrays are emitted as [] rather than null (project policy).
		info.Features = []string{}
	}
	writeRESTJSON(w, http.StatusOK, info)
}

// handleInstallPlatformLicense accepts a signed deployment license envelope,
// verifies it with the embedded/mounted vendor public key and atomically
// replaces the current deployment license. Only the platform super admin can
// reach this route; a tenant-scope envelope is rejected by the service.
func (h *Hub) handleInstallPlatformLicense(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLicenseUploadBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "LICENSE_INVALID",
			"license envelope is too large or unreadable")
		return
	}
	file, err := license.ParseFile(body)
	if h.writeLicenseError(w, err) {
		return
	}
	if _, err := h.licenseAdmin.InstallDeployment(r.Context(), file, actorFromContext(r)); h.writeLicenseError(w, err) {
		return
	}
	info, err := h.licenseAdmin.DeploymentInfo(r.Context())
	if h.writeLicenseError(w, err) {
		return
	}
	if info.Features == nil {
		info.Features = []string{}
	}
	writeRESTJSON(w, http.StatusOK, info)
}

func (h *Hub) handleTenantLicenseState(w http.ResponseWriter, r *http.Request) {
	state, err := h.licenseAdmin.TenantState(r.Context(), r.PathValue("tenant"), time.Now().UTC())
	if h.writeLicenseError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, state)
}

func (h *Hub) handleSuspendTenant(w http.ResponseWriter, r *http.Request) {
	var body suspendTenantBody
	if !decodeBody(w, r, &body) {
		return
	}
	actor := actorFromContext(r)
	if err := h.licenseAdmin.SuspendTenant(r.Context(), r.PathValue("tenant"), body.Reason, actor); h.writeLicenseError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handleResumeTenant(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r)
	if err := h.licenseAdmin.ResumeTenant(r.Context(), r.PathValue("tenant"), actor); h.writeLicenseError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handleListUsage(w http.ResponseWriter, r *http.Request) {
	from, to, ok := usageRange(w, r)
	if !ok {
		return
	}
	rows, err := h.licenseAdmin.ListUsage(r.Context(), from, to)
	if h.writeLicenseError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, rows)
}

func (h *Hub) handleTenantUsage(w http.ResponseWriter, r *http.Request) {
	from, to, ok := usageRange(w, r)
	if !ok {
		return
	}
	rows, err := h.licenseAdmin.TenantUsage(r.Context(), r.PathValue("tenant"), from, to)
	if h.writeLicenseError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, rows)
}

func usageRange(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -29)
	to := today
	query := r.URL.Query()
	if value := query.Get("from"); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "USAGE_INVALID_DATE", "invalid from date")
			return time.Time{}, time.Time{}, false
		}
		from = parsed
	}
	if value := query.Get("to"); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "USAGE_INVALID_DATE", "invalid to date")
			return time.Time{}, time.Time{}, false
		}
		to = parsed
	}
	return from, to, true
}

func actorFromContext(r *http.Request) string {
	p := contextPrincipal(r.Context())
	if p == nil {
		return "platform-cli"
	}
	if p.Email != "" {
		return p.Email
	}
	return p.UserID
}

func (h *Hub) writeLicenseError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, license.ErrReadOnly), errors.Is(err, license.ErrSuspended):
		writeRESTError(w, http.StatusForbidden, "LICENSE_RESTRICTED", err.Error())
	case errors.Is(err, license.ErrInvalidLicense), errors.Is(err, license.ErrInvalidSignature):
		writeRESTError(w, http.StatusBadRequest, "LICENSE_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "LICENSE_OPERATION_FAILED", err.Error())
	}
	return true
}
