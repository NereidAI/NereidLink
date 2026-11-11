// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/quota"
	"github.com/nereidai/nereidlink/internal/registry"
)

// RegistryAdmin is the management surface for profiles, devices and credentials.
type RegistryAdmin interface {
	ListProfiles(ctx context.Context, tenantID string) ([]registry.ProfileDTO, error)
	CreateProfile(ctx context.Context, tenantID string, input registry.ProfileDTO) (registry.ProfileDTO, error)
	GetProfile(ctx context.Context, tenantID, profileID string) (registry.ProfileDTO, error)
	UpdateProfile(ctx context.Context, tenantID, profileID string, input registry.ProfileDTO) (registry.ProfileDTO, error)
	DeleteProfile(ctx context.Context, tenantID, profileID string) error

	ListDevices(ctx context.Context, tenantID string, filter registry.DeviceFilter, page, pageSize int) (apipage.Page[registry.DeviceDTO], error)
	CreateDevice(ctx context.Context, tenantID string, input registry.DeviceDTO) (registry.DeviceDTO, error)
	// BulkImportDevices creates every valid row of a CSV upload and reports
	// per-row failures instead of aborting the batch.
	BulkImportDevices(ctx context.Context, tenantID, actorID string, reader io.Reader) (registry.BulkImportResult, error)
	// PreviewDeviceImport validates a CSV upload and returns parsed rows
	// without writing.
	PreviewDeviceImport(ctx context.Context, tenantID string, reader io.Reader) (registry.ImportPreview, error)
	// BulkImportProfiles creates every valid profile row of a CSV upload.
	BulkImportProfiles(ctx context.Context, tenantID, actorID string, reader io.Reader) (registry.BulkImportProfilesResult, error)
	// PreviewProfileImport validates a profile CSV upload without writing.
	PreviewProfileImport(ctx context.Context, tenantID string, reader io.Reader) (registry.ImportPreview, error)
	GetDevice(ctx context.Context, tenantID, deviceID string) (registry.DeviceDTO, error)
	UpdateDevice(ctx context.Context, tenantID, deviceID string, input registry.DeviceDTO) (registry.DeviceDTO, error)
	DeleteDevice(ctx context.Context, tenantID, deviceID string) error
	// VisibleDeviceIDs intersects a list of ids with the devices visible to
	// the caller under its data scope. all=true means the caller is
	// unrestricted and receives every device (a true wildcard).
	VisibleDeviceIDs(ctx context.Context, tenantID string, ids []string) (map[string]struct{}, bool, error)

	ListAttributes(ctx context.Context, tenantID, deviceID, scope string) ([]registry.AttributeDTO, error)
	PutAttribute(ctx context.Context, tenantID, deviceID, scope, key string, value json.RawMessage) error
	DeleteAttribute(ctx context.Context, tenantID, deviceID, scope, key string) error

	ListCredentials(ctx context.Context, tenantID, deviceID string) ([]registry.CredentialDTO, error)
	CreateCredential(ctx context.Context, tenantID, deviceID string, input registry.CredentialDTO, actorID, ip string) (registry.CredentialDTO, error)
	UpdateCredentialSecret(ctx context.Context, tenantID, deviceID, credentialID, secret, actorID, ip string) error
	DeleteCredential(ctx context.Context, tenantID, deviceID, credentialID, actorID, ip string) error

	// Credential lifecycle operations from ADR-0009.
	SetCredentialStatus(ctx context.Context, tenantID, deviceID, credentialID, status, actorID, ip string) (registry.CredentialDTO, error)
	RotateCredential(ctx context.Context, tenantID, deviceID, credentialID, newSecret, newExpiresAt, actorID, ip string) (registry.CredentialDTO, error)
	RevokeAllCredentials(ctx context.Context, tenantID, deviceID, actorID, ip string) (int64, error)
	BumpCredentialEpoch(ctx context.Context, tenantID, actorID, ip string) (int64, error)
	RevealCredential(ctx context.Context, tenantID, deviceID, credentialID, actorID, ip string) (registry.CredentialDTO, error)
}

// credentialSecretBody updates a credential secret.
type credentialSecretBody struct {
	CredentialsValue string `json:"credentials_value"`
}

// credentialStatusBody switches a credential between ACTIVE and
// DISABLED. EXPIRED is scanner-owned and rejected.
type credentialStatusBody struct {
	Status string `json:"status"`
}

// credentialRotateBody carries the replacement secret and optional
// expiry of a rotation; an empty expires_at keeps the current value.
type credentialRotateBody struct {
	CredentialsValue string `json:"credentials_value"`
	ExpiresAt        string `json:"expires_at,omitempty"`
}

// handleProfiles lists or creates profiles.
func (h *Hub) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		profiles, err := h.registryAdmin.ListProfiles(r.Context(), tenant)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, profiles)
	case http.MethodPost:
		var input registry.ProfileDTO
		if !decodeBody(w, r, &input) {
			return
		}
		profile, err := h.registryAdmin.CreateProfile(r.Context(), tenant, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, profile)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleProfile reads, updates or deletes one profile.
func (h *Hub) handleProfile(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	profileID := r.PathValue("profile")
	switch r.Method {
	case http.MethodGet:
		profile, err := h.registryAdmin.GetProfile(r.Context(), tenant, profileID)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, profile)
	case http.MethodPut:
		var input registry.ProfileDTO
		if !decodeBody(w, r, &input) {
			return
		}
		profile, err := h.registryAdmin.UpdateProfile(r.Context(), tenant, profileID, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, profile)
	case http.MethodDelete:
		if err := h.registryAdmin.DeleteProfile(r.Context(), tenant, profileID); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDevices lists or creates devices.
func (h *Hub) handleDevices(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		page, pageSize := parsePagination(r)
		devices, err := h.registryAdmin.ListDevices(r.Context(), tenant, registry.DeviceFilter{
			Search:             r.URL.Query().Get("search"),
			ProfileID:          r.URL.Query().Get("profile_id"),
			CustomerID:         r.URL.Query().Get("customer_id"),
			GroupID:            r.URL.Query().Get("group_id"),
			State:              r.URL.Query().Get("state"),
			GatewayOnly:        r.URL.Query().Get("gateway") == "true",
			ManagedByGatewayID: r.URL.Query().Get("managed_by"),
			Type:               r.URL.Query().Get("type"),
			Connection:         r.URL.Query().Get("connection"),
		}, page, pageSize)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, devices)
	case http.MethodPost:
		var input registry.DeviceDTO
		if !decodeBody(w, r, &input) {
			return
		}
		if err := h.licenseAdmin.EnsureTenantCanCreateDevice(r.Context(), tenant); h.writeLicenseError(w, err) {
			return
		}
		device, err := h.registryAdmin.CreateDevice(r.Context(), tenant, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, device)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDevice reads, updates or deletes one device.
func (h *Hub) handleDevice(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	deviceID := r.PathValue("device")
	switch r.Method {
	case http.MethodGet:
		device, err := h.registryAdmin.GetDevice(r.Context(), tenant, deviceID)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, device)
	case http.MethodPut:
		var input registry.DeviceDTO
		if !decodeBody(w, r, &input) {
			return
		}
		device, err := h.registryAdmin.UpdateDevice(r.Context(), tenant, deviceID, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, device)
	case http.MethodDelete:
		if err := h.registryAdmin.DeleteDevice(r.Context(), tenant, deviceID); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// bulkDecommissionBody is the request body of the bulk decommission action.
// RecallDays is optional and omitted when the operator accepts the default
// recall window.
type bulkDecommissionBody struct {
	DeviceIDs []string `json:"device_ids"`
	// RecallDays overrides the default recall window; zero means default.
	RecallDays int `json:"recall_days"`
}

// handleBulkDecommission decommissions every selected device in one call.
// It is the bulk counterpart of DELETE /devices/{device}: the same
// non-destructive transition, so no device row is physically removed.
func (h *Hub) handleBulkDecommission(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	var body bulkDecommissionBody
	if !decodeBody(w, r, &body) {
		return
	}
	var recall time.Duration
	if body.RecallDays > 0 {
		recall = time.Duration(body.RecallDays) * 24 * time.Hour
	}
	result, err := h.ownershipAdmin.BulkDecommissionDevice(
		r.Context(), tenant, body.DeviceIDs, recall, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// parseBulkUpload extracts a CSV reader from either a multipart file upload
// (CSV or XLSX, with XLSX converted to CSV) or a raw text/csv body. It is
// shared by the device and profile import/preview handlers so format
// handling never drifts between them.
func parseBulkUpload(w http.ResponseWriter, r *http.Request) (io.Reader, bool) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// nolint:gosec
		if err := r.ParseMultipartForm(registry.MaxBulkImportBytes); err != nil {
			writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "failed to parse multipart form")
			return nil, false
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "missing file field")
			return nil, false
		}
		// Closing is the caller's responsibility through the returned reader;
		// the form file is fully buffered when it is converted below.
		if strings.HasSuffix(strings.ToLower(header.Filename), ".xlsx") ||
			strings.HasSuffix(header.Header.Get("Content-Type"), "spreadsheetml.sheet") {
			csvReader, convErr := xlsxToCSVReader(file)
			_ = file.Close()
			if convErr != nil {
				writeRESTError(w, http.StatusBadRequest, "XLSX_PARSE_FAILED", convErr.Error())
				return nil, false
			}
			return csvReader, true
		}
		return file, true
	}
	if strings.HasPrefix(contentType, "text/csv") {
		return r.Body, true
	}
	writeRESTError(w, http.StatusBadRequest, "INVALID_CONTENT_TYPE",
		"expected text/csv or multipart/form-data with file field")
	return nil, false
}

// handleBulkImport creates many devices from an uploaded CSV or XLSX file.
//
// Accepts either raw CSV text (text/csv) or a multipart/form-data file upload
// for XLSX. The response reports per-row failures so partial success is
// visible to the operator.
func (h *Hub) handleBulkImport(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	// The license gate runs once for the whole batch: it is a tenant-state
	// check, not a per-device quota, and the registry re-checks nothing.
	if err := h.licenseAdmin.EnsureTenantCanCreateDevice(r.Context(), tenant); h.writeLicenseError(w, err) {
		return
	}

	reader, ok := parseBulkUpload(w, r)
	if !ok {
		return
	}

	result, err := h.registryAdmin.BulkImportDevices(r.Context(), tenant, actorID(r), reader)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleBulkImportPreview validates a device upload and returns the parsed
// rows with per-row validation, without writing anything.
func (h *Hub) handleBulkImportPreview(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	reader, ok := parseBulkUpload(w, r)
	if !ok {
		return
	}
	preview, err := h.registryAdmin.PreviewDeviceImport(r.Context(), r.PathValue("tenant"), reader)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, preview)
}

// handleProfileBulkImport creates many profiles from an uploaded CSV file.
func (h *Hub) handleProfileBulkImport(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	reader, ok := parseBulkUpload(w, r)
	if !ok {
		return
	}
	result, err := h.registryAdmin.BulkImportProfiles(r.Context(), r.PathValue("tenant"), actorID(r), reader)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleProfileBulkImportPreview validates a profile upload without writing.
func (h *Hub) handleProfileBulkImportPreview(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	reader, ok := parseBulkUpload(w, r)
	if !ok {
		return
	}
	preview, err := h.registryAdmin.PreviewProfileImport(r.Context(), r.PathValue("tenant"), reader)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, preview)
}

// handleBulkImportTemplate streams the fillable import template. The format is
// chosen by the query string so one endpoint serves both spreadsheet flavours
// an operator may have: xlsx when they want validation and documentation,
// csv when a script is the uploader.
func (h *Hub) handleBulkImportTemplate(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "xlsx"
	}

	var (
		body        []byte
		err         error
		contentType string
		filename    string
	)
	switch format {
	case "xlsx":
		body, err = renderBulkImportXLSX()
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		filename = "nereidlink-device-import-template.xlsx"
	case "csv":
		body, err = renderBulkImportCSV()
		contentType = "text/csv; charset=utf-8"
		filename = "nereidlink-device-import-template.csv"
	default:
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"format must be xlsx or csv")
		return
	}
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "TEMPLATE_RENDER_FAILED", err.Error())
		return
	}
	_ = tenant // tenant only scopes the route; the template is identical for all.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	// The template is generated per request and never cached, so a client or
	// proxy must not serve a stale column list after an upgrade.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleCredentials lists or creates credentials.
func (h *Hub) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	deviceID := r.PathValue("device")
	switch r.Method {
	case http.MethodGet:
		credentials, err := h.registryAdmin.ListCredentials(r.Context(), tenant, deviceID)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, credentials)
	case http.MethodPost:
		var input registry.CredentialDTO
		if !decodeBody(w, r, &input) {
			return
		}
		credential, err := h.registryAdmin.CreateCredential(
			r.Context(), tenant, deviceID, input, actorID(r), requestIP(r))
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, credential)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleCredentialSecret returns the stored credential value to a tenant
// administrator. Hashed PASSWORD rows return revealable=false.
func (h *Hub) handleCredentialSecret(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) || !requireMethod(w, r, http.MethodGet) {
		return
	}
	credential, err := h.registryAdmin.RevealCredential(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), r.PathValue("credential"),
		actorID(r), requestIP(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, credential)
}

// handleCredential updates or deletes one credential.
func (h *Hub) handleCredential(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenant := r.PathValue("tenant")
	deviceID := r.PathValue("device")
	credentialID := r.PathValue("credential")
	switch r.Method {
	case http.MethodPut:
		var body credentialSecretBody
		if !decodeBody(w, r, &body) {
			return
		}
		if err := h.registryAdmin.UpdateCredentialSecret(
			r.Context(), tenant, deviceID, credentialID, body.CredentialsValue,
			actorID(r), requestIP(r)); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := h.registryAdmin.DeleteCredential(
			r.Context(), tenant, deviceID, credentialID,
			actorID(r), requestIP(r)); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleCredentialStatus enables or disables one credential. Disabling
// ends the live session; the device re-authenticates with its other
// valid credential if it still holds one.
func (h *Hub) handleCredentialStatus(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body credentialStatusBody
	if !decodeBody(w, r, &body) {
		return
	}
	credential, err := h.registryAdmin.SetCredentialStatus(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), r.PathValue("credential"),
		body.Status, actorID(r), requestIP(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, credential)
}

// handleCredentialRotate replaces a credential secret in place and
// rebinds its expiry, then forces the device to reconnect.
func (h *Hub) handleCredentialRotate(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body credentialRotateBody
	if !decodeBody(w, r, &body) {
		return
	}
	credential, err := h.registryAdmin.RotateCredential(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), r.PathValue("credential"),
		body.CredentialsValue, body.ExpiresAt, actorID(r), requestIP(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, credential)
}

// handleCredentialRevokeAll disables every credential of one device.
func (h *Hub) handleCredentialRevokeAll(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) || !requireMethod(w, r, http.MethodPost) {
		return
	}
	revoked, err := h.registryAdmin.RevokeAllCredentials(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), actorID(r), requestIP(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]int64{"revoked": revoked})
}

// handleCredentialEpoch bumps the per-tenant credential epoch used for
// emergency bulk invalidation once the credential cache exists.
func (h *Hub) handleCredentialEpoch(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) || !requireMethod(w, r, http.MethodPost) {
		return
	}
	epoch, err := h.registryAdmin.BumpCredentialEpoch(r.Context(),
		r.PathValue("tenant"), actorID(r), requestIP(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]int64{"credential_epoch": epoch})
}

// requireMethod rejects mismatched verbs with 405.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// registryReady verifies the admin service is wired.
func (h *Hub) registryReady(w http.ResponseWriter) bool {
	if h.registryAdmin != nil {
		return true
	}
	writeRESTError(w, http.StatusServiceUnavailable, "REGISTRY_SERVICE_UNAVAILABLE",
		"registry management service is not enabled")
	return false
}

// decodeBody decodes a JSON request body.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "malformed JSON body")
		return false
	}
	return true
}

// writeRegistryError maps domain errors to HTTP status codes.
func writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, registry.ErrConflict):
		writeRESTError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, registry.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	case errors.Is(err, quota.ErrExceeded):
		writeRESTError(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "REGISTRY_OPERATION_FAILED", err.Error())
	}
}
