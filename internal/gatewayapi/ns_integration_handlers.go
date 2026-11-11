// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ns_integration_handlers.go serves the NereidSphere (NS) suite admin
// console. The call arrives through the NS reverse proxy (/p/link/...),
// which strips any inbound identity headers, injects the verified
// X-User-* identity and stamps X-NS-Integration-Secret (the same shared
// secret used for the ticket exchange) on exactly this path. Because the
// product origin is also directly reachable, the secret is mandatory:
// X-User-Roles alone must never be trusted.

package gatewayapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/sso"
)

// NSIntegration is the surface the NS console tenant picker drives.
type NSIntegration interface {
	// ListTenants returns the ACTIVE tenants for the console picker.
	ListTenants(ctx context.Context) ([]sso.TenantInfo, error)
	// IntegrationSecret is the shared secret NS stamps on this path.
	IntegrationSecret() string
}

// SetNSIntegration wires the NS console integration endpoints.
func (h *Hub) SetNSIntegration(svc NSIntegration) {
	h.nsIntegration = svc
}

// handleNSIntegrationTenants answers GET /v1/integration/ns/tenants for
// the NS suite admin console.
func (h *Hub) handleNSIntegrationTenants(w http.ResponseWriter, r *http.Request) {
	if h.nsIntegration == nil {
		http.NotFound(w, r)
		return
	}

	// Constant-time shared-secret proof that the call came through NS.
	// An empty header or configured secret fails outright.
	secret := r.Header.Get("X-NS-Integration-Secret")
	expected := h.nsIntegration.IntegrationSecret()
	if expected == "" || secret == "" ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(expected)) != 1 {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid integration credentials")
		return
	}

	// Coarse authorization: only a suite admin may enumerate tenants.
	if !headerHasSuiteRole(r, "admin") {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"suite administrator role required")
		return
	}

	tenants, err := h.nsIntegration.ListTenants(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "INTERNAL",
			"could not list tenants")
		return
	}
	writeRESTJSON(w, http.StatusOK, tenants)
}

// headerHasSuiteRole checks the comma-joined X-User-Roles value injected
// by the NS gateway for an exact, case-sensitive role entry.
func headerHasSuiteRole(r *http.Request, want string) bool {
	for _, part := range strings.Split(r.Header.Get("X-User-Roles"), ",") {
		if strings.TrimSpace(part) == want {
			return true
		}
	}
	return false
}
