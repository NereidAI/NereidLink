// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ownership_handlers.go exposes the ADR-0013 ownership and lifecycle
// use cases over REST: customers and device groups, single-use claim
// tokens, claiming, transfer and decommission/reactivate. Every write
// records an ownership history row server-side; handlers only adapt the
// transport.

package gatewayapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/registry"
)

// OwnershipAdmin extends the registry management surface with the
// ADR-0013 operations. It is satisfied by *registry.Admin.
type OwnershipAdmin interface {
	ListCustomers(ctx context.Context, tenantID string) ([]registry.CustomerDTO, error)
	CreateCustomer(ctx context.Context, tenantID string, input registry.CustomerDTO) (registry.CustomerDTO, error)
	GetCustomer(ctx context.Context, tenantID, customerID string) (registry.CustomerDTO, error)
	UpdateCustomer(ctx context.Context, tenantID, customerID string, input registry.CustomerDTO) (registry.CustomerDTO, error)
	DeleteCustomer(ctx context.Context, tenantID, customerID string) error

	ListGroups(ctx context.Context, tenantID string) ([]registry.GroupDTO, error)
	CreateGroup(ctx context.Context, tenantID string, input registry.GroupDTO) (registry.GroupDTO, error)
	UpdateGroup(ctx context.Context, tenantID, groupID string, input registry.GroupDTO) error
	DeleteGroup(ctx context.Context, tenantID, groupID string) error

	DeviceCountsByCustomer(ctx context.Context, tenantID string) (map[string]int64, error)
	DeviceCountsByGroup(ctx context.Context, tenantID string) (map[string]int64, error)

	IssueClaimToken(ctx context.Context, tenantID, deviceID string, ttl time.Duration) (registry.ClaimTokenResult, error)
	ClaimDevice(ctx context.Context, tenantID, deviceID string, input registry.ClaimInput, actorID string) (registry.DeviceDTO, error)
	TransferDevice(ctx context.Context, tenantID, deviceID string, input registry.TransferInput, actorID string) (registry.DeviceDTO, error)
	DecommissionDevice(ctx context.Context, tenantID, deviceID string, recall time.Duration, actorID string) (registry.DeviceDTO, error)
	// BulkDecommissionDevice applies the same transition to many devices in
	// one transaction, reporting per-device skips instead of aborting.
	BulkDecommissionDevice(ctx context.Context, tenantID string, deviceIDs []string, recall time.Duration, actorID string) (registry.BulkDecommissionResult, error)
	ReactivateDevice(ctx context.Context, tenantID, deviceID, actorID string) (registry.DeviceDTO, error)

	// QuarantineDevice enters incident-response quarantine with a
	// structured reason code and an optional note.
	QuarantineDevice(ctx context.Context, tenantID, deviceID, reasonCode, note, actorID string) (registry.DeviceDTO, error)
	// ReleaseFromQuarantine returns the device to ACTIVE once the
	// incident is resolved.
	ReleaseFromQuarantine(ctx context.Context, tenantID, deviceID, note, actorID string) (registry.DeviceDTO, error)
}

// SetOwnershipAdmin wires the ownership lifecycle service.
func (h *Hub) SetOwnershipAdmin(admin OwnershipAdmin) {
	h.ownershipAdmin = admin
}

// claimTokenBody optionally overrides the token lifetime.
type claimTokenBody struct {
	TTL string `json:"ttl"`
}

// handleCustomers lists or creates customers.
func (h *Hub) handleCustomers(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		customers, err := h.ownershipAdmin.ListCustomers(r.Context(), tenant)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, customers)
	case http.MethodPost:
		var input registry.CustomerDTO
		if !decodeBody(w, r, &input) {
			return
		}
		customer, err := h.ownershipAdmin.CreateCustomer(r.Context(), tenant, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, customer)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleCustomer reads, updates or deletes one customer.
func (h *Hub) handleCustomer(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	customerID := r.PathValue("customer")
	switch r.Method {
	case http.MethodGet:
		customer, err := h.ownershipAdmin.GetCustomer(r.Context(), tenant, customerID)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, customer)
	case http.MethodPut:
		var input registry.CustomerDTO
		if !decodeBody(w, r, &input) {
			return
		}
		customer, err := h.ownershipAdmin.UpdateCustomer(r.Context(), tenant, customerID, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, customer)
	case http.MethodDelete:
		if err := h.ownershipAdmin.DeleteCustomer(r.Context(), tenant, customerID); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDeviceGroups lists or creates groups.
func (h *Hub) handleDeviceGroups(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		groups, err := h.ownershipAdmin.ListGroups(r.Context(), tenant)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, groups)
	case http.MethodPost:
		var input registry.GroupDTO
		if !decodeBody(w, r, &input) {
			return
		}
		group, err := h.ownershipAdmin.CreateGroup(r.Context(), tenant, input)
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, group)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDeviceGroup updates or deletes one group.
func (h *Hub) handleDeviceGroup(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	groupID := r.PathValue("group")
	switch r.Method {
	case http.MethodPut:
		var input registry.GroupDTO
		if !decodeBody(w, r, &input) {
			return
		}
		if err := h.ownershipAdmin.UpdateGroup(r.Context(), tenant, groupID, input); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := h.ownershipAdmin.DeleteGroup(r.Context(), tenant, groupID); err != nil {
			writeRegistryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleClaimToken issues a single-use claim token.
func (h *Hub) handleClaimToken(w http.ResponseWriter, r *http.Request) {
	var body claimTokenBody
	if r.ContentLength != 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	ttl, err := parseOptionalDuration(body.TTL)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	result, err := h.ownershipAdmin.IssueClaimToken(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), ttl)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusCreated, result)
}

// handleClaim consumes a claim token and claims the device.
func (h *Hub) handleClaim(w http.ResponseWriter, r *http.Request) {
	var input registry.ClaimInput
	if !decodeBody(w, r, &input) {
		return
	}
	device, err := h.ownershipAdmin.ClaimDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), input, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// handleTransfer moves a device to another customer/group.
func (h *Hub) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var input registry.TransferInput
	if !decodeBody(w, r, &input) {
		return
	}
	device, err := h.ownershipAdmin.TransferDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), input, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// decommissionBody optionally overrides the recall window.
type decommissionBody struct {
	Recall string `json:"recall"`
}

// handleDecommission retires a device.
func (h *Hub) handleDecommission(w http.ResponseWriter, r *http.Request) {
	var body decommissionBody
	if r.ContentLength != 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	recall, err := parseOptionalDuration(body.Recall)
	if err != nil {
		writeRESTError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	device, err := h.ownershipAdmin.DecommissionDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), recall, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// handleReactivate restores a decommissioned device.
func (h *Hub) handleReactivate(w http.ResponseWriter, r *http.Request) {
	device, err := h.ownershipAdmin.ReactivateDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// quarantineBody is the request body of quarantine entry and release.
// Entry uses ReasonCode plus an optional Note; release uses Note only.
type quarantineBody struct {
	// ReasonCode is the structured incident code. It is required on entry
	// and ignored on release.
	ReasonCode string `json:"reason_code"`
	// Note is the free-text detail. It is required for the "other" reason
	// code and for every release.
	Note string `json:"note"`
}

// handleQuarantine moves a device into incident-response quarantine.
func (h *Hub) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	var body quarantineBody
	if !decodeBody(w, r, &body) {
		return
	}
	device, err := h.ownershipAdmin.QuarantineDevice(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"),
		body.ReasonCode, body.Note, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// handleQuarantineRelease lifts incident-response quarantine after the
// administrator records why the incident is resolved.
func (h *Hub) handleQuarantineRelease(w http.ResponseWriter, r *http.Request) {
	var body quarantineBody
	if !decodeBody(w, r, &body) {
		return
	}
	device, err := h.ownershipAdmin.ReleaseFromQuarantine(r.Context(),
		r.PathValue("tenant"), r.PathValue("device"), body.Note, actorID(r))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, device)
}

// handleQuarantineReasons lists the accepted quarantine reason codes so the
// UI does not hard-code a vocabulary that the backend can reject.
func (h *Hub) handleQuarantineReasons(w http.ResponseWriter, r *http.Request) {
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"reason_codes":  registry.QuarantineReasonCodes(),
		"note_required": []string{registry.ReasonOther},
	})
}

// handleCustomerDeviceCounts returns non-decommissioned device counts
// keyed by customer id.
func (h *Hub) handleCustomerDeviceCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := h.ownershipAdmin.DeviceCountsByCustomer(
		r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, counts)
}

// handleGroupDeviceCounts returns non-decommissioned device counts keyed
// by device group id.
func (h *Hub) handleGroupDeviceCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := h.ownershipAdmin.DeviceCountsByGroup(
		r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, counts)
}

// actorID returns the authenticated user id for history attribution.
func actorID(r *http.Request) string {
	if p := contextPrincipal(r.Context()); p != nil {
		return p.UserID
	}
	return ""
}

// parseOptionalDuration parses an empty or Go-duration string; empty
// means "use the service default".
func parseOptionalDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}
