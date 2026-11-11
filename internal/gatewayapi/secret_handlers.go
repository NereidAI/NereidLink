// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secret_handlers.go exposes the per-tenant secret vault that backs
// secret:// references in connector config (ADR-0026, migration 0047). The
// value is write-only over the API: list/get return metadata only and never
// echo the secret. Reads of connector config need connector:read, writes and
// deletes need connector:write.

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/secretbinding"
	"github.com/nereidai/nereidlink/internal/secretcatalog"
	"github.com/nereidai/nereidlink/internal/secretdelete"
	"github.com/nereidai/nereidlink/internal/secrets"
)

// SecretStore is the vault management surface.
type SecretStore interface {
	Put(ctx context.Context, tenantID, name, value string) (secrets.Metadata, error)
	PutDocument(ctx context.Context, tenantID, name, secretType, typeVersion string, document map[string]any) (secrets.Metadata, error)
	Get(ctx context.Context, tenantID, name string) (secrets.Metadata, error)
	List(ctx context.Context, tenantID string) ([]secrets.Metadata, error)
	Delete(ctx context.Context, tenantID, name string) error
}

// DocumentValidator validates typed documents against the type catalog.
type DocumentValidator interface {
	Validate(ctx context.Context, tenantID, secretType, typeVersion string, doc json.RawMessage) error
}

// SetSecretStore wires the secret vault.
func (h *Hub) SetSecretStore(store SecretStore) {
	h.secretStore = store
}

// SetSecretValidator wires the typed-document validator.
func (h *Hub) SetSecretValidator(validator DocumentValidator) {
	h.secretValidator = validator
}

// SecretDeletionAdmin is the deletion protection surface: list gateways
// holding a copy or force the deletion (withdraw all, then delete).
type SecretDeletionAdmin interface {
	References(ctx context.Context, tenantID, secretName string) ([]secretbinding.Reference, error)
	ForceDelete(ctx context.Context, tenantID, secretName string, actor secretdelete.Actor) (secretdelete.PendingDelete, error)
}

// SetSecretDeletionAdmin wires the deletion guard service.
func (h *Hub) SetSecretDeletionAdmin(admin SecretDeletionAdmin) {
	h.secretDeletion = admin
}

// secretDeleteBody carries the forced-delete options on a DELETE. An empty
// body (the traditional DELETE) means a normal, guarded deletion.
type secretDeleteBody struct {
	// Force selects forced deletion: withdraw all delivered copies.
	Force bool `json:"force,omitempty"`
	// StepUpID is the consumed TOTP step-up challenge; required with force.
	StepUpID string `json:"step_up_id,omitempty"`
}

// StepUp action key for forced vault deletion.
const secretForceDeleteAction = "edge_secret:force-delete"

// secretWriteBody carries either a legacy single value or a typed document.
type secretWriteBody struct {
	Value string `json:"value"`

	SecretType  string         `json:"secret_type,omitempty"`
	TypeVersion string         `json:"type_version,omitempty"`
	Document    map[string]any `json:"document,omitempty"`
}

// handleTenantSecrets lists metadata for the tenant vault.
func (h *Hub) handleTenantSecrets(w http.ResponseWriter, r *http.Request) {
	if h.secretStore == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_VAULT_UNAVAILABLE",
			"secret vault is not enabled")
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	items, err := h.secretStore.List(r.Context(), r.PathValue("tenant"))
	if err != nil {
		writeSecretError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleTenantSecret serves PUT (create/update), GET metadata and DELETE for
// one named secret.
func (h *Hub) handleTenantSecret(w http.ResponseWriter, r *http.Request) {
	if h.secretStore == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_VAULT_UNAVAILABLE",
			"secret vault is not enabled")
		return
	}
	tenant := r.PathValue("tenant")
	name := r.PathValue("secret")
	switch r.Method {
	case http.MethodGet:
		md, err := h.secretStore.Get(r.Context(), tenant, name)
		if err != nil {
			writeSecretError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, md)
	case http.MethodPut:
		var body secretWriteBody
		if !decodeBody(w, r, &body) {
			return
		}
		if len(body.Document) > 0 {
			raw, err := json.Marshal(body.Document)
			if err != nil {
				writeRESTError(w, http.StatusBadRequest, "SECRET_INVALID_DOCUMENT",
					"document must be a JSON object")
				return
			}
			if h.secretValidator != nil {
				if err := h.secretValidator.Validate(r.Context(), tenant,
					body.SecretType, body.TypeVersion, raw); err != nil {
					writeSecretError(w, err)
					return
				}
			}
			md, err := h.secretStore.PutDocument(r.Context(), tenant, name,
				body.SecretType, body.TypeVersion, body.Document)
			if err != nil {
				writeSecretError(w, err)
				return
			}
			h.auditSecretChange(r, name, "rotate_document", body.SecretType)
			writeRESTJSON(w, http.StatusOK, md)
			return
		}
		md, err := h.secretStore.Put(r.Context(), tenant, name, body.Value)
		if err != nil {
			writeSecretError(w, err)
			return
		}
		h.auditSecretChange(r, name, "rotate_value", "")
		writeRESTJSON(w, http.StatusOK, md)
	case http.MethodDelete:
		h.handleSecretDelete(w, r, tenant, name)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSecretDelete applies deletion protection (ticket 07): a secret still
// delivered to one or more gateways blocks a normal delete with the gateway
// list; a forced delete consumes a fresh human TOTP step-up, withdraws every
// binding, and defers the hard-delete until withdrawals are terminal.
func (h *Hub) handleSecretDelete(w http.ResponseWriter, r *http.Request, tenant, name string) {
	if h.secretDeletion == nil {
		// Deletion guard unavailable: fail closed rather than deleting an
		// in-use secret unchecked.
		writeRESTError(w, http.StatusServiceUnavailable, "SECRET_DELETE_UNAVAILABLE",
			"secret deletion guard is not enabled")
		return
	}
	var body secretDeleteBody
	// Tolerate the traditional body-less DELETE; only decode when one is
	// actually present.
	if r.ContentLength != 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	refs, err := h.secretDeletion.References(r.Context(), tenant, name)
	if err != nil {
		writeSecretError(w, err)
		return
	}

	if len(refs) > 0 && !body.Force {
		h.auditSecretDeleteBlocked(r, name, len(refs))
		writeRESTJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{
				"code":    "SECRET_IN_USE",
				"message": "withdraw this secret from the listed gateways before deleting it, or force the deletion",
			},
			"secret_name": name,
			"references":  refs,
		})
		return
	}

	if len(refs) > 0 {
		principal := contextPrincipal(r.Context())
		if principal == nil || principal.Machine {
			writeRESTError(w, http.StatusForbidden, "SECRET_HUMAN_ONLY",
				"forced deletion requires a human operator")
			return
		}
		if body.StepUpID == "" {
			writeRESTError(w, http.StatusPreconditionRequired, "STEP_UP_REQUIRED",
				"complete the authenticator code before forced deletion")
			return
		}
		if h.stepUp == nil {
			writeRESTError(w, http.StatusServiceUnavailable, "STEP_UP_UNAVAILABLE",
				"step-up is not enabled")
			return
		}
		if err := h.stepUp.Consume(r.Context(), tenant, principal.UserID,
			body.StepUpID, secretForceDeleteAction, requestIP(r)); err != nil {
			writeStepUpError(w, err)
			return
		}
		pending, err := h.secretDeletion.ForceDelete(r.Context(), tenant, name,
			secretdelete.Actor{UserID: principal.UserID, Email: principal.Email})
		if err != nil {
			writeSecretError(w, err)
			return
		}
		// Fast path (no references remained) already hard-deleted the row;
		// an empty PendingDelete means terminal deletion.
		if pending.SecretName == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeRESTJSON(w, http.StatusAccepted, pending)
		return
	}

	if err := h.secretStore.Delete(r.Context(), tenant, name); err != nil {
		writeSecretError(w, err)
		return
	}
	h.auditSecretChange(r, name, "delete", "")
	w.WriteHeader(http.StatusNoContent)
}

// auditSecretDeleteBlocked records a rejected normal deletion so the
// protection event is visible in the chained log.
func (h *Hub) auditSecretDeleteBlocked(r *http.Request, name string, gatewayCount int) {
	if h.auditReader == nil {
		return
	}
	principal := contextPrincipal(r.Context())
	event := audit.Input{
		EventType:    audit.EventCredential,
		Action:       "delete-blocked",
		ResourceType: "tenant_secret",
		ResourceID:   name,
		IP:           requestIP(r),
		Details: map[string]any{
			"name":          name,
			"gateway_count": gatewayCount,
		},
	}
	if principal != nil {
		event.TenantID = r.PathValue("tenant")
		event.ActorID = principal.UserID
		event.ActorName = principal.Email
	}
	if _, err := h.auditReader.Append(r.Context(), event); err != nil {
		slog.Error("failed to audit blocked secret delete", "error", err)
	}
}

// auditSecretChange writes a chained credential event naming the key and
// action only; document content is never included.
func (h *Hub) auditSecretChange(r *http.Request, name, action, secretType string) {
	if h.auditReader == nil {
		return
	}
	principal := contextPrincipal(r.Context())
	event := audit.Input{
		ActorName:    r.PathValue("secret"),
		EventType:    audit.EventCredential,
		Action:       action,
		ResourceType: "tenant_secret",
		ResourceID:   name,
		IP:           requestIP(r),
		Details:      map[string]any{"name": name},
	}
	if secretType != "" {
		event.Details["secret_type"] = secretType
	}
	if principal != nil {
		event.TenantID = r.PathValue("tenant")
		event.ActorID = principal.UserID
		event.ActorName = principal.Email
	}
	if _, err := h.auditReader.Append(r.Context(), event); err != nil {
		slog.Error("failed to audit secret change", "error", err)
	}
}

func writeSecretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "SECRET_NOT_FOUND", "secret not found")
	case errors.Is(err, secrets.ErrInvalidName):
		writeRESTError(w, http.StatusBadRequest, "SECRET_INVALID_NAME",
			"name must be 1-128 chars, start alphanumeric, then alphanumeric/_/./-")
	case errors.Is(err, secrets.ErrEmptyValue):
		writeRESTError(w, http.StatusBadRequest, "SECRET_EMPTY_VALUE", "value must not be empty")
	case errors.Is(err, secretcatalog.ErrFieldsViolation):
		writeRESTError(w, http.StatusBadRequest, "SECRET_DOCUMENT_INVALID", err.Error())
	case errors.Is(err, secretcatalog.ErrInvalidSchema):
		writeRESTError(w, http.StatusBadRequest, "SECRET_TYPE_INVALID", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "SECRET_OPERATION_FAILED", err.Error())
	}
}
