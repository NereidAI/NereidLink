// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// authorize.go is the gateway enforcement point of ADR-0022: every API
// request is authenticated from its bearer token, mapped to one
// permission point and checked against the caller's tenant grant. No
// decision is made inside individual handlers; internal routes
// (/v1/me device endpoints, module enrollment) use their own auth.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/userapikey"
)

// principalContext carries the resolved caller for handlers that
// record actor-specific audit events.
type principalContext struct{}

// contextPrincipal returns the authenticated principal, if any.
func contextPrincipal(ctx context.Context) *rbac.Principal {
	p, _ := ctx.Value(principalContext{}).(*rbac.Principal)
	return p
}

// authorize wraps the mux with authentication and permission checks.
func (h *Hub) authorize(next http.Handler, wsPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isExemptPath(r, wsPath) ||
			(h.ssoCallbackPath != "" && r.URL.Path == h.ssoCallbackPath) {
			next.ServeHTTP(w, r)
			return
		}

		var principal rbac.Principal
		if apiKey := strings.TrimSpace(r.Header.Get("X-Api-Key")); apiKey != "" {
			authenticated, ok := h.authenticateAPIKey(w, r, apiKey)
			if !ok {
				return
			}
			principal = *authenticated
		} else {
			var err error
			principal, err = h.rbacResolver.FromToken(r.Context(),
				h.identityService, bearerToken(r))
			if err != nil {
				writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED",
					"missing or invalid access token")
				return
			}
		}

		if !enforceRoute(w, r, &principal) {
			return
		}

		// Resolve the tenant grant into an executable data scope and carry
		// it on ctx. Permission points say what a caller may do; the scope
		// says which rows they may touch, and enforcement happens in the
		// store layer where the query is built. Resolving here — once, after
		// the tenant is known — keeps every downstream query consistent and
		// avoids re-walking the group subtree per query.
		tenantID := routeTenant(r)
		if grant, ok := principal.Tenants[tenantID]; ok {
			scope, err := h.rbacResolver.DataScopeFor(r.Context(), tenantID, grant)
			if err != nil {
				// A membership pointing at a missing or cross-tenant
				// customer/group is a data-integrity fault, not a caller
				// error. Refuse the request rather than fall back to
				// tenant-wide, which would turn corrupt data into a leak.
				writeRESTError(w, http.StatusForbidden, "DATA_SCOPE_INVALID",
					"your data scope is no longer valid; ask an administrator")
				return
			}
			r = r.WithContext(rbac.ContextWithScope(r.Context(), scope))
		} else {
			// No grant for this tenant: the route check already refused
			// anything but an exempt or platform-admin path. Bind an
			// explicit unrestricted scope so store code cannot mistake the
			// absence of a scope for a caller that still needs narrowing.
			r = r.WithContext(rbac.ContextWithScope(r.Context(),
				rbac.UnrestrictedScope(tenantID)))
		}

		// Handlers that write actor-specific events read it from ctx.
		ctx := context.WithValue(r.Context(), principalContext{}, &principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Hub) authenticateAPIKey(w http.ResponseWriter,
	r *http.Request, plaintext string) (*rbac.Principal, bool) {
	if h.apiKeyAuthenticator == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE",
			"user API keys are not enabled")
		return nil, false
	}
	result, err := h.apiKeyAuthenticator.Authenticate(r.Context(),
		plaintext, requestIP(r))
	if err != nil {
		switch {
		case errors.Is(err, userapikey.ErrLocked):
			writeRESTError(w, http.StatusTooManyRequests, "API_KEY_LOCKED", err.Error())
		case errors.Is(err, userapikey.ErrUserDisabled):
			writeRESTError(w, http.StatusForbidden, "USER_DISABLED", err.Error())
		case errors.Is(err, userapikey.ErrTenantInactive):
			writeRESTError(w, http.StatusForbidden, "TENANT_INACTIVE", err.Error())
		case errors.Is(err, userapikey.ErrScopeNotAllowed):
			writeRESTError(w, http.StatusForbidden, "API_KEY_SCOPE_NOT_ALLOWED", err.Error())
		case errors.Is(err, userapikey.ErrInvalidCredentials):
			writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED",
				"missing or invalid API key")
		default:
			writeRESTError(w, http.StatusInternalServerError, "API_KEY_OPERATION_FAILED",
				err.Error())
		}
		return nil, false
	}
	if h.rbacResolver == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "AUTH_SERVICE_UNAVAILABLE",
			"authorization is not enabled")
		return nil, false
	}
	principal, err := h.rbacResolver.PrincipalForUser(r.Context(), result.UserID)
	if err != nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid API key")
		return nil, false
	}
	principal.APIKeyID = result.KeyID
	if len(result.Scopes) > 0 {
		principal.Scopes = result.Scopes
	}
	return &principal, true
}

// routeTenant extracts the tenant id from a /v1/tenants/{tenant}/... path.
// It returns "" for paths that are not tenant-scoped (health, auth session,
// platform administration), which the caller treats as "no tenant scope".
func routeTenant(r *http.Request) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "tenants" {
		return parts[2]
	}
	return ""
}

// isExemptPath lists routes with their own authentication or no
// authentication requirement.
func isExemptPath(r *http.Request, wsPath string) bool {
	path := r.URL.Path

	switch {
	case path == "/healthz" || path == "/metrics":
		return true
	case path == "/v1/auth/captcha" || path == "/v1/auth/login" ||
		path == "/v1/auth/refresh" ||
		path == "/v1/auth/token" ||
		path == "/v1/auth/bootstrap" ||
		path == "/v1/auth/bootstrap-status" ||
		path == "/v1/auth/mfa/verify" ||
		path == "/v1/auth/oidc/status" ||
		path == "/v1/auth/oidc/login" ||
		path == "/v1/auth/oidc/callback" ||
		path == "/v1/auth/sso/status" ||
		path == "/v1/integration/ns/tenants":
		// The NS ticket callback path is deployment-configured
		// (sso.callback_path, default /auth/ns-callback) and is exempted
		// dynamically in authorize; non-/v1 paths are exempt below anyway.
		// The integration endpoint authenticates the NS-stamped shared
		// secret inside its own handler.
		return true
	case strings.HasPrefix(path, "/v1/me/"):
		return true
	case path == "/v1/blobs/get":
		// Local backend download URLs carry their own HMAC signature.
		return true
	case path == "/v1/modules/register":
		return true
	case strings.HasPrefix(path, "/v1/modules/") && strings.HasSuffix(path, "/heartbeat"):
		return true
	case r.Method == http.MethodGet && path == wsPath:
		// WS authenticates from its query token in serveWS.
		return true
	case !strings.HasPrefix(path, "/v1/"):
		return true
	}
	return false
}

// enforceRoute applies the route-specific decision and reports whether
// the request may proceed.
func enforceRoute(w http.ResponseWriter, r *http.Request, p *rbac.Principal) bool {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	// Auth session routes only require a valid token (already parsed).
	if len(parts) >= 2 && parts[0] == "v1" && parts[1] == "auth" {
		return true
	}

	// Tenant member administration (ADR-0030 delegation): a platform
	// admin may manage every tenant, while a TENANT_ADMIN manages the
	// members of their own tenant. Other roles (ENGINEER/READ_ONLY/
	// CUSTOMER_USER) are refused even inside their own tenant.
	if len(parts) >= 4 && parts[0] == "v1" && parts[1] == "tenants" &&
		parts[3] == "members" {
		if p.PlatformAdmin {
			return true
		}
		if p.HasPoint(parts[2], rbac.PointMembersAdmin) {
			return true
		}
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"tenant member administration requires tenant administrator")
		return false
	}

	// Platform administration routes require the platform-admin flag.
	if isPlatformRoute(parts) {
		if !p.PlatformAdmin {
			writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
				"platform administration required")
			return false
		}
		return true
	}

	// Everything else is /v1/tenants/{tenant}/...
	if len(parts) < 3 || parts[1] != "tenants" {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN", "unknown route scope")
		return false
	}
	tenant := parts[2]
	point := requiredPoint(r.Method, parts[3:])

	// The vault accepts both the legacy connector points (single-value
	// secrets used by connector config) and the ADR-0034 edge_secret points
	// (typed documents). Either grant authorizes; the handler decides which
	// shape the body carries.
	if len(parts) >= 4 && parts[3] == "secrets" {
		alt := rbac.PointEdgeSecretRead
		if r.Method != http.MethodGet {
			alt = rbac.PointEdgeSecretWrite
		}
		if !p.HasPoint(tenant, point) && !p.HasPoint(tenant, alt) {
			writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
				"missing permission point: "+point)
			return false
		}
		return true
	}

	// Credential administration is closed to non-interactive principals
	// regardless of their scopes: a long-lived credential must never be
	// able to mint another credential or widen its own access.
	if len(parts) >= 4 && parts[3] == "service-accounts" &&
		(p.Machine || p.APIKeyID != "") {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"credential administration requires an interactive user session")
		return false
	}
	if len(parts) >= 4 && parts[3] == "api-keys" &&
		(p.Machine || p.APIKeyID != "") {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"API-key administration requires an interactive user session")
		return false
	}

	// Gateway operations (ADR-0035) are human-only: the catalog, task
	// history and invocation are all closed to a machine principal even
	// when its scopes contain the tenant-admin wildcard, because dangerous
	// actions require a TOTP step-up a machine cannot perform.
	if p.Machine && isGatewayOperationRoute(parts[3:]) {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"gateway operations require a human operator")
		return false
	}

	if !p.HasPoint(tenant, point) {
		writeRESTError(w, http.StatusForbidden, "FORBIDDEN",
			"missing permission point: "+point)
		return false
	}
	return true
}

// isPlatformRoute recognizes cross-tenant administration paths.
func isPlatformRoute(parts []string) bool {
	if len(parts) < 2 || parts[0] != "v1" {
		return false
	}
	// Audit query/verification is platform administration.
	if parts[1] == "audit" {
		return true
	}
	// Platform trust-anchor management is platform administration.
	if parts[1] == "platform" {
		return true
	}
	// High-risk operation orders are platform administration.
	if parts[1] == "ops" {
		return true
	}
	// Cross-tenant metering is platform administration.
	if parts[1] == "usage" {
		return true
	}
	if parts[1] == "tenants" && len(parts) == 2 {
		return true
	}
	// /v1/tenants/{id} and /retry operate across tenants.
	if parts[1] == "tenants" && len(parts) == 3 {
		return true
	}
	if parts[1] == "tenants" && len(parts) == 4 &&
		(parts[3] == "retry" || parts[3] == "suspend" ||
			parts[3] == "resume" || parts[3] == "license" ||
			parts[3] == "usage") {
		return true
	}
	// Module management under a tenant prefix is platform-level:
	// /v1/tenants/{tenant}/modules...
	if len(parts) >= 4 && parts[1] == "tenants" && parts[3] == "modules" {
		return true
	}
	// Device quarantine entry and release are incident response for
	// platform admins: /v1/tenants/{tenant}/devices/{device}/quarantine
	// [/release].
	if (len(parts) == 6 || len(parts) == 8) && parts[1] == "tenants" &&
		parts[3] == "devices" && parts[5] == "quarantine" {
		return true
	}
	return false
}

// requiredPoint maps a tenant-scoped route to its permission point.
// rest is the path segments after /v1/tenants/{tenant}.
func requiredPoint(method string, rest []string) string {
	if len(rest) == 0 {
		return rbac.PointStats
	}

	write := method != http.MethodGet
	switch rest[0] {
	case "stats", "overview":
		return rbac.PointStats
	case "presence":
		return rbac.PointPresenceRead
	case "telemetry-dlq":
		return rbac.PointStats
	case "notifications":
		return rbac.PointNotificationRead
	case "storage-hooks":
		return ternary(write, rbac.PointHookWrite, rbac.PointHookRead)
	case "dashboards":
		return ternary(write, rbac.PointDashboardWrite, rbac.PointDashboardRead)
	case "escalation-chains":
		return ternary(write, rbac.PointEscalationWrite, rbac.PointEscalationRead)
	case "quota":
		return rbac.PointQuotaRead
	case "retention":
		// PUT additionally requires platform admin, enforced in the
		// handler; all roles may at least pass the tenant-scope read gate.
		return rbac.PointRetentionRead
	case "blob":
		// Signing a media object reference is a device-data read.
		return rbac.PointDeviceRead
	case "discovery-policy":
		// Per-tenant/per-gateway auto-provisioning policy (ADR-0026);
		// gateway-scoped requests reach gatewayPoint instead.
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "discovery-candidates":
		// Human-review approval queue for auto-provisioning (migration
		// 0046): reads need device:read, approve/reject device:write.
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "secrets":
		// Per-tenant secret vault backing connector secret:// references
		// (migration 0047); reuse connector points.
		return ternary(write, rbac.PointConnectorWrite, rbac.PointConnectorRead)
	case "forwarders", "forwarder-dlq", "forwarder-deliveries":
		// Third-party data forwarding (ADR-0018): forwarder, delivery-log
		// and DLQ/replay management share the forwarder points.
		return ternary(write, rbac.PointForwarderWrite, rbac.PointForwarderRead)
	case "companion-endpoints":
		// Generic companion connector (companion-subscription/v1): endpoint
		// registration and desired/read-back subscription visibility.
		return ternary(write, rbac.PointCompanionWrite, rbac.PointCompanionRead)
	case "link-episodes":
		// Read-only link outage history; alarm-like visibility.
		return rbac.PointAlarmRead
	case "rule-chain-groups", "rule-chains", "rule-nodes":
		return ternary(write, rbac.PointRuleChainWrite, rbac.PointRuleChainRead)
	case "business-templates", "business-instances":
		return ternary(write, rbac.PointBusinessWrite, rbac.PointBusinessRead)
	case "profiles":
		return ternary(write, rbac.PointProfileWrite, rbac.PointProfileRead)
	case "protocol-families":
		// Global device-service catalog (ADR-0033): rows are global but the
		// route is tenant-scoped purely to attach the normal RBAC gate; reuse
		// profile points because the catalog drives profile connector forms.
		return ternary(write, rbac.PointProfileWrite, rbac.PointProfileRead)
	case "alarms":
		if len(rest) >= 3 {
			switch rest[2] {
			case "ack":
				return rbac.PointAlarmAck
			case "assign":
				return rbac.PointAlarmAssign
			case "comments":
				if write {
					return rbac.PointAlarmComment
				}
				return rbac.PointAlarmRead
			}
		}
		return rbac.PointAlarmRead
	case "commands":
		return rbac.PointDeviceRead
	case "device-events":
		// Tenant-wide read of the append-only device domain-event ledger;
		// per-device data scope is enforced in the query.
		return rbac.PointDeviceRead
	case "command-tasks":
		if method == http.MethodPost {
			return rbac.PointDeviceCommand
		}
		return rbac.PointDeviceRead
	case "probes":
		return ternary(write, rbac.PointProbeWrite, rbac.PointProbeRead)
	case "ota-tasks":
		// Firmware deployment is an operation on gateway-attached devices;
		// reuse device points rather than mint a separate RBAC scope.
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "firmware":
		// Platform-hosted firmware repository (ADR-0006b): repository
		// management reuses device write/read points, same as ota-tasks.
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "devices":
		return devicePoint(method, rest[1:])
	case "customers", "device-groups":
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "gateways":
		// /gateways/{gateway}/connectors[/...] manages edge connector
		// desired specs (ADR-0026).
		return gatewayPoint(method, rest[1:])
	case "connector-schemas":
		// Edge-registered connector config-schema catalog (read only).
		return rbac.PointConnectorRead
	case "edge-secret-types":
		// Edge secret type descriptor catalog (ADR-0034).
		return ternary(write, rbac.PointEdgeSecretWrite, rbac.PointEdgeSecretRead)
	case "step-up":
		// Operation-time TOTP step-up (ADR-0035). Route gate only proves
		// tenant access; the action's real point is checked in the handler.
		return rbac.PointStats
	case "gateway-operations":
		// Gateway operation catalog (ADR-0035). Reads need read point;
		// writes (including disable) use the exec point.
		if write {
			return rbac.PointEdgeOperationExec
		}
		return rbac.PointEdgeOperationRead
	case "customer-device-counts", "group-device-counts":
		return rbac.PointDeviceRead
	case "credential-epoch":
		// Emergency bulk invalidation is credential administration.
		return rbac.PointCredentialWrite
	case "api-keys":
		// Tenant-wide user API-key administration. Self-service lives
		// under /v1/auth/api-keys and is scoped to the caller's user id.
		if write {
			return rbac.PointExternalAccessWrite
		}
		return rbac.PointExternalAccessRead
	case "service-accounts":
		// Machine-identity administration is tenant-admin only: the
		// write and read points are granted solely by the '*' wildcard,
		// so an ENGINEER or an automation token cannot mint credentials.
		if write {
			return rbac.PointServiceAccountWrite
		}
		return rbac.PointServiceAccountRead
	}
	return rbac.PointDeviceRead
}

// gatewayPoint narrows a gateway route by its sub-resource.
func gatewayPoint(method string, sub []string) string {
	write := method != http.MethodGet
	if len(sub) >= 2 && (sub[1] == "connectors" || sub[1] == "connectors-reconcile") {
		return ternary(write, rbac.PointConnectorWrite, rbac.PointConnectorRead)
	}
	// Edge model inventory (ADR-0039) is read-only and reuses the connector
	// read scope: the same operator manages connectors and the models they
	// bind to.
	if len(sub) >= 2 && sub[1] == "models" {
		return rbac.PointConnectorRead
	}
	// Secret delivery bindings reuse the edge_secret permission points
	// (ADR-0034): the vault and delivery share one scope family.
	if len(sub) >= 2 && sub[1] == "secret-bindings" {
		return ternary(write, rbac.PointEdgeSecretWrite, rbac.PointEdgeSecretRead)
	}
	// Whole-gateway operation invocation and task history use the
	// operation points (ADR-0035).
	if len(sub) >= 2 && (sub[1] == "operations" || sub[1] == "operation-tasks") {
		return ternary(write, rbac.PointEdgeOperationExec, rbac.PointEdgeOperationRead)
	}
	// EdgeX profiles are cached connector content but adopting one creates a
	// native device profile, so the adopt action is governed by profile
	// points rather than connector points.
	if len(sub) >= 2 && sub[1] == "edge-profiles" {
		return ternary(write, rbac.PointProfileWrite, rbac.PointProfileRead)
	}
	return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
}

// isGatewayOperationRoute recognizes the human-only ADR-0035 routes from the
// path segment after the tenant: gateway-operations/* or
// gateways/{id}/operations|operation-tasks/*.
func isGatewayOperationRoute(rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	if rest[0] == "gateway-operations" {
		return true
	}
	return rest[0] == "gateways" && len(rest) >= 3 &&
		(rest[2] == "operations" || rest[2] == "operation-tasks")
}

// devicePoint narrows the device route by its sub-resource.
func devicePoint(method string, sub []string) string {
	write := method != http.MethodGet
	if len(sub) == 0 {
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	}
	switch sub[0] {
	case "commands":
		if method == http.MethodPost {
			return rbac.PointDeviceCommand
		}
		return rbac.PointDeviceRead
	case "command-catalog":
		// Whitelist declarations are device configuration; dispatching
		// itself remains device:command above.
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "shadow":
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	case "credentials":
		return ternary(write, rbac.PointCredentialWrite, rbac.PointCredentialRead)
	case "attributes":
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	default:
		return ternary(write, rbac.PointDeviceWrite, rbac.PointDeviceRead)
	}
}

// ternary selects one of two points.
func ternary(condition bool, whenWrite, whenRead string) string {
	if condition {
		return whenWrite
	}
	return whenRead
}
