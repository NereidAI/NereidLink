// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package rbac implements the permission model from ADR-0022:
//
//	decision = permission point × resource type × data scope.
//
// Principals are resolved from a verified access token and carry, per
// tenant membership, a data scope and one or more fixed built-in roles.
// Role permission points union; the widest scope wins. All decisions
// are made at the gateway (gatewayapi) — internal services trust each
// other through machine identity added later.
package rbac

// Permission point constants. The set is version-fixed: plugins
// declare their own points when registered; nothing here is guessed.
const (
	// PointAll is the tenant-admin wildcard.
	PointAll = "*"

	// Device points.
	PointDeviceRead    = "device:read"
	PointDeviceWrite   = "device:write"
	PointDeviceCommand = "device:command"

	// Credential points (names are permission points, not secrets).
	PointCredentialRead  = "credential:read"  //nolint:gosec
	PointCredentialWrite = "credential:write" //nolint:gosec

	// Profile points.
	PointProfileRead  = "profile:read"
	PointProfileWrite = "profile:write"

	// Rule chain points.
	PointRuleChainRead  = "rulechain:read"
	PointRuleChainWrite = "rulechain:write"

	// Alarm points.
	PointAlarmRead    = "alarm:read"
	PointAlarmAck     = "alarm:ack"
	PointAlarmAssign  = "alarm:assign"
	PointAlarmComment = "alarm:comment"

	// Storage hook points.
	PointHookRead  = "hook:read"
	PointHookWrite = "hook:write"

	// Dashboard points.
	PointDashboardRead  = "dashboard:read"
	PointDashboardWrite = "dashboard:write"

	// Escalation chain points.
	PointEscalationRead  = "escalation:read"
	PointEscalationWrite = "escalation:write"

	// Edge connector instance points (ADR-0026 desired/reported lifecycle).
	PointConnectorRead  = "connector:read"
	PointConnectorWrite = "connector:write"

	// On-demand gateway probe points (ADR-0026 section 9).
	PointProbeRead  = "probes:read"
	PointProbeWrite = "probes:write"

	// Edge secret delivery points (ADR-0034): management of credential
	// descriptors and the credentials delivered to gateways.
	PointEdgeSecretRead  = "edge_secret:read"
	PointEdgeSecretWrite = "edge_secret:write"

	PointEdgeOperationRead = "edge_operation:read"
	PointEdgeOperationExec = "edge_operation:exec"

	// Third-party data forwarding points (ADR-0018 forwarder management).
	PointForwarderRead  = "forwarder:read"
	PointForwarderWrite = "forwarder:write"

	// Business orchestration points cover immutable templates, generation-safe
	// instances, declarative expansion and deployment lifecycle actions.
	PointBusinessRead  = "business:read"
	PointBusinessWrite = "business:write"

	// Companion connector points (companion-subscription/v1): registration of
	// external companion endpoints and read of desired/read-back state.
	PointCompanionRead  = "companion:read"
	PointCompanionWrite = "companion:write"

	// Tenant visibility points.
	PointStats        = "stats:read"
	PointPresenceRead = "presence:read"

	// Notification point.
	PointNotificationRead = "notification:read"

	// Quota point (read of own tenant quotas).
	PointQuotaRead = "quota:read"

	// Retention point (read of own tenant retention policy).
	PointRetentionRead = "retention:read"

	// Service account points (ADR-0021 machine identity administration).
	// Only tenant administration (the '*' wildcard) holds them: an
	// automation identity must not be able to mint more automation
	// identities.
	PointServiceAccountRead  = "serviceaccount:read"  //nolint:gosec
	PointServiceAccountWrite = "serviceaccount:write" //nolint:gosec

	// PointMembersAdmin governs a tenant's own member administration
	// (ADR-0030 delegation): list/create members and change roles,
	// credentials and enable state. It is held solely by the tenant
	// administrator role via the PointAll wildcard; platform admins
	// bypass the point check. ENGINEER/READ_ONLY/CUSTOMER_USER never
	// hold it.
	PointMembersAdmin = "members:admin"

	// External-access points govern tenant-wide administration of a
	// user's long-lived API keys. Self-service key management remains
	// under the authenticated user's account routes; these points cover
	// administrator visibility and revocation across the tenant.
	PointExternalAccessRead  = "external-access:read"
	PointExternalAccessWrite = "external-access:write"
)

// Built-in role keys, matching the CHECK constraint in migration 0014.
const (
	// RolePlatformAdmin is represented outside memberships (the
	// app_user.is_platform_admin flag) and listed here for mapping.
	RolePlatformAdmin = "PLATFORM_ADMIN"

	// RoleTenantAdmin holds every tenant point.
	RoleTenantAdmin = "TENANT_ADMIN"

	// RoleEngineer operates device configuration and commands.
	RoleEngineer = "ENGINEER"

	// RoleCustomerUser sees/acts on its scoped devices and alarms.
	RoleCustomerUser = "CUSTOMER_USER"

	// RoleReadOnly only reads.
	RoleReadOnly = "READ_ONLY"

	// RoleServiceAccount exists for explicit enumerated automation.
	RoleServiceAccount = "SERVICE_ACCOUNT"
)

// rolePoints maps each fixed role to its permission points. The
// Tenant Admin wildcard is resolved in Principal.HasPoint; other sets
// are explicit so a point can never leak into a role by accident.
var rolePoints = map[string][]string{
	RoleTenantAdmin: {PointAll},
	RoleEngineer: {
		PointDeviceRead, PointDeviceWrite, PointDeviceCommand,
		PointCredentialRead,
		PointProfileRead, PointProfileWrite,
		PointRuleChainRead, PointRuleChainWrite,
		PointAlarmRead, PointAlarmAck, PointAlarmAssign, PointAlarmComment,
		PointHookRead, PointHookWrite,
		PointDashboardRead, PointDashboardWrite,
		PointEscalationRead, PointEscalationWrite,
		PointConnectorRead, PointConnectorWrite,
		PointProbeRead, PointProbeWrite,
		PointEdgeSecretRead, PointEdgeSecretWrite,
		PointEdgeOperationRead, PointEdgeOperationExec,
		PointForwarderRead, PointForwarderWrite,
		PointBusinessRead, PointBusinessWrite,
		PointCompanionRead, PointCompanionWrite,
		PointStats, PointPresenceRead, PointNotificationRead,
		PointQuotaRead, PointRetentionRead,
	},
	RoleCustomerUser: {
		PointDeviceRead, PointDeviceCommand,
		PointProbeRead, PointProbeWrite,
		PointAlarmRead, PointAlarmAck, PointAlarmComment, PointNotificationRead,
		PointDashboardRead,
		PointEscalationRead,
		PointQuotaRead, PointRetentionRead,
	},
	RoleReadOnly: {
		PointDeviceRead, PointProfileRead, PointRuleChainRead,
		PointAlarmRead, PointHookRead,
		PointDashboardRead,
		PointEscalationRead,
		PointConnectorRead,
		PointProbeRead,
		PointEdgeSecretRead,
		PointEdgeOperationRead,
		PointForwarderRead,
		PointBusinessRead,
		PointCompanionRead,
		PointStats, PointPresenceRead, PointNotificationRead,
		PointQuotaRead, PointRetentionRead,
	},
	RoleServiceAccount: {},
}

// KnownPermissionPoints is the version-fixed whitelist of values a
// machine-identity scope (ADR-0021) may carry, including the tenant-admin
// wildcard. It is the Go-side source of the CHECK constraint in migration
// 0037: a service account scope must never name a string this layer
// cannot enforce. Keep the two lists in lockstep.
var KnownPermissionPoints = []string{
	PointAll,
	PointDeviceRead, PointDeviceWrite, PointDeviceCommand,
	PointCredentialRead, PointCredentialWrite,
	PointProfileRead, PointProfileWrite,
	PointRuleChainRead, PointRuleChainWrite,
	PointAlarmRead, PointAlarmAck, PointAlarmAssign, PointAlarmComment,
	PointHookRead, PointHookWrite,
	PointDashboardRead, PointDashboardWrite,
	PointEscalationRead, PointEscalationWrite,
	PointConnectorRead, PointConnectorWrite,
	PointProbeRead, PointProbeWrite,
	PointEdgeSecretRead, PointEdgeSecretWrite,
	PointEdgeOperationRead, PointEdgeOperationExec,
	PointForwarderRead, PointForwarderWrite,
	PointBusinessRead, PointBusinessWrite,
	PointCompanionRead, PointCompanionWrite,
	PointStats, PointPresenceRead, PointNotificationRead,
	PointQuotaRead, PointRetentionRead,
	PointServiceAccountRead, PointServiceAccountWrite,
	PointExternalAccessRead, PointExternalAccessWrite,
}

// IsKnownPoint reports whether point may appear in a machine scope.
func IsKnownPoint(point string) bool {
	for _, known := range KnownPermissionPoints {
		if known == point {
			return true
		}
	}
	return false
}

// MachineGrantablePoints is the subset of permission points a machine
// token scope is allowed to carry. Identity-administration points are
// deliberately excluded: an automation identity must never be able to
// mint or rotate another automation identity, even when an operator
// requests it or grants the '*' wildcard. Gateway operations
// (ADR-0035) are human-only because dangerous actions require a TOTP
// step-up a machine cannot perform, so both edge_operation points are
// excluded. The gateway additionally refuses machine principals on the
// invoke and service-account routes, so this list is the data-layer
// half of the same invariants.
var MachineGrantablePoints = func() []string {
	out := make([]string, 0, len(KnownPermissionPoints))
	for _, point := range KnownPermissionPoints {
		if point == PointServiceAccountRead ||
			point == PointServiceAccountWrite ||
			point == PointExternalAccessRead ||
			point == PointExternalAccessWrite ||
			point == PointEdgeOperationRead ||
			point == PointEdgeOperationExec {
			continue
		}
		out = append(out, point)
	}
	return out
}()

// IsMachineGrantable reports whether point may be assigned to a service
// account scope.
func IsMachineGrantable(point string) bool {
	for _, allowed := range MachineGrantablePoints {
		if allowed == point {
			return true
		}
	}
	return false
}
