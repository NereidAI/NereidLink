// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// member_handlers.go exposes tenant member administration: list/create
// members and per-member roles, credentials, enable state and removal.
// Authorization (enforceRoute) admits a platform admin for any tenant
// and a TENANT_ADMIN only inside their own tenant (PointMembersAdmin);
// the routes sit under a tenant path despite the cross-tenant admin
// use.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nereidai/nereidlink/internal/usermgmt"
)

// MemberAdmin is the tenant member management surface. *usermgmt.Service
// satisfies it.
type MemberAdmin interface {
	ListMembers(ctx context.Context, tenantID string) ([]usermgmt.Member, error)
	CreateMember(ctx context.Context, tenantID string, input usermgmt.CreateMemberInput) (usermgmt.CreateResult, error)
	SetRoles(ctx context.Context, tenantID, userID string, roles []string) (usermgmt.Member, error)
	SetDataScope(ctx context.Context, tenantID, userID, scope, scopeID string) (usermgmt.Member, error)
	ResetPassword(ctx context.Context, tenantID, userID, newPassword string) (string, error)
	SetDisabled(ctx context.Context, tenantID, userID string, disabled bool) (usermgmt.Member, error)
	RemoveMember(ctx context.Context, tenantID, userID string) error
}

// SetMemberAdmin wires the member management service.
func (h *Hub) SetMemberAdmin(admin MemberAdmin) {
	h.memberAdmin = admin
}

// createMemberBody carries the new account fields; empty password
// makes the service generate one, returned in generated_password.
type createMemberBody struct {
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`

	// DataScope defaults to TENANT. CUSTOMER and GROUP narrow the member to
	// one subtree and require DataScopeID.
	DataScope string `json:"data_scope"`

	// DataScopeID anchors a CUSTOMER or GROUP scope.
	DataScopeID string `json:"data_scope_id"`
}

// rolesBody carries the replacement role set.
type rolesBody struct {
	Roles []string `json:"roles"`
}

// dataScopeBody carries the replacement data scope of one membership.
type dataScopeBody struct {
	DataScope string `json:"data_scope"`

	// DataScopeID anchors a CUSTOMER or GROUP scope and must be empty for
	// TENANT.
	DataScopeID string `json:"data_scope_id"`
}

// passwordBody carries an optional replacement credential; empty
// makes the service generate one.
type passwordBody struct {
	Password string `json:"password"`
}

// enabledBody carries the desired enable state.
type enabledBody struct {
	Enabled bool `json:"enabled"`
}

// memberCallContext injects the authenticated operator and verifies
// the service is wired.
func (h *Hub) memberCallContext(w http.ResponseWriter, r *http.Request) (context.Context, MemberAdmin, bool) {
	if h.memberAdmin == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "MEMBER_SERVICE_UNAVAILABLE",
			"member management is not enabled")
		return nil, nil, false
	}
	ctx := usermgmt.ContextWithActor(r.Context(), actorID(r))
	return ctx, h.memberAdmin, true
}

// handleMembers lists or creates tenant members.
func (h *Hub) handleMembers(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok {
		return
	}
	tenant := r.PathValue("tenant")
	switch r.Method {
	case http.MethodGet:
		members, err := admin.ListMembers(ctx, tenant)
		if err != nil {
			writeMemberError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusOK, members)
	case http.MethodPost:
		var body createMemberBody
		if !decodeBody(w, r, &body) {
			return
		}
		result, err := admin.CreateMember(ctx, tenant, usermgmt.CreateMemberInput{
			Email:       body.Email,
			DisplayName: body.DisplayName,
			Password:    body.Password,
			Roles:       body.Roles,
			DataScope:   body.DataScope,
			DataScopeID: body.DataScopeID,
		})
		if err != nil {
			writeMemberError(w, err)
			return
		}
		writeRESTJSON(w, http.StatusCreated, map[string]any{
			"member":             result.Member,
			"generated_password": result.GeneratedPassword,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleMemberRoles replaces the role set of one member.
func (h *Hub) handleMemberRoles(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body rolesBody
	if !decodeBody(w, r, &body) {
		return
	}
	member, err := admin.SetRoles(ctx, r.PathValue("tenant"), r.PathValue("user"), body.Roles)
	if err != nil {
		writeMemberError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, member)
}

// handleMemberDataScope replaces the data scope of one member.
//
// Scope is administration, not self-service: widening a member from a
// customer or group to the whole tenant is a permission change and belongs
// beside the role endpoints rather than in the member's own profile.
func (h *Hub) handleMemberDataScope(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body dataScopeBody
	if !decodeBody(w, r, &body) {
		return
	}
	member, err := admin.SetDataScope(ctx, r.PathValue("tenant"),
		r.PathValue("user"), body.DataScope, body.DataScopeID)
	if err != nil {
		writeMemberError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, member)
}

// handleMemberPassword resets the credential of one member and
// returns the new plaintext (supplied or generated).
func (h *Hub) handleMemberPassword(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body passwordBody
	if !decodeBody(w, r, &body) {
		return
	}
	password, err := admin.ResetPassword(ctx, r.PathValue("tenant"), r.PathValue("user"), body.Password)
	if err != nil {
		writeMemberError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]string{"password": password})
}

// handleMemberEnabled enables or disables one member account.
func (h *Hub) handleMemberEnabled(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok || !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body enabledBody
	if !decodeBody(w, r, &body) {
		return
	}
	member, err := admin.SetDisabled(ctx, r.PathValue("tenant"), r.PathValue("user"), !body.Enabled)
	if err != nil {
		writeMemberError(w, err)
		return
	}
	writeRESTJSON(w, http.StatusOK, member)
}

// handleMember removes one member and its tenant-local account.
func (h *Hub) handleMember(w http.ResponseWriter, r *http.Request) {
	ctx, admin, ok := h.memberCallContext(w, r)
	if !ok || !requireMethod(w, r, http.MethodDelete) {
		return
	}
	if err := admin.RemoveMember(ctx, r.PathValue("tenant"), r.PathValue("user")); err != nil {
		writeMemberError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeMemberError maps usermgmt errors to HTTP status codes.
func writeMemberError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, usermgmt.ErrNotFound):
		writeRESTError(w, http.StatusNotFound, "MEMBER_NOT_FOUND", err.Error())
	case errors.Is(err, usermgmt.ErrConflict):
		writeRESTError(w, http.StatusConflict, "MEMBER_CONFLICT", err.Error())
	case errors.Is(err, usermgmt.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "MEMBER_INVALID_ARGUMENT", err.Error())
	case errors.Is(err, usermgmt.ErrProtected):
		writeRESTError(w, http.StatusForbidden, "MEMBER_PROTECTED", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "MEMBER_OPERATION_FAILED", err.Error())
	}
}
