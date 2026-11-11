// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

// Control frame op values shared between the server and the documented
// WebSocket protocol. They are wire identifiers and therefore defined once
// as constants rather than configuration values.
const (
	// OpSubscribe adds a resource subscription.
	OpSubscribe = "subscribe"

	// OpUnsubscribe removes a resource subscription.
	OpUnsubscribe = "unsubscribe"

	// OpResume requests replay after reconnection.
	OpResume = "resume"

	// OpReauth supplies a renewed access token.
	OpReauth = "reauth"
)

// Resource type identifiers accepted in subscription frames.
const (
	// ResourceTelemetry subscribes to normalized device telemetry.
	ResourceTelemetry = "telemetry"

	// ResourcePresence subscribes to device connection state changes.
	ResourcePresence = "presence"

	// ResourceAlarm subscribes to alarm lifecycle events.
	ResourceAlarm = "alarm"

	// ResourceNotification subscribes to in-app notifications.
	ResourceNotification = "notification"

	// ResourceCommandTask subscribes to batch command progress.
	ResourceCommandTask = "command_task"

	// ResourceProbeTask subscribes to on-demand probe task progress.
	ResourceProbeTask = "probe_task"

	// ResourceOTATask subscribes to firmware OTA task progress.
	ResourceOTATask = "ota_task"

	// ResourceDeviceEvent subscribes to device-published domain events
	// (v1/me/events), narrowed per device by data scope like alarms.
	ResourceDeviceEvent = "device_event"

	// ResourceConnector subscribes to edge connector lifecycle events
	// (desired writes, edge-reported status and manifest reconciliation).
	// It is tenant-scoped: the console re-fetches the connector list/reconcile
	// view for whichever gateway an event names.
	ResourceConnector = "connector"

	// ResourceModelInventory subscribes to edge model-inventory change
	// notifications (ADR-0039 manifest models reconciliation, staleness and
	// runtime-plane alarm state). Tenant-scoped: the console re-fetches the
	// gateway's model list on a frame. It requires connector:read.
	ResourceModelInventory = "model_inventory"

	// ResourceSecretBinding subscribes to edge secret delivery lifecycle
	// events (desired writes, withdrawals, edge-reported status). It is
	// tenant-scoped: the console re-fetches the gateway's binding list.
	ResourceSecretBinding = "secret_binding"

	// ResourceMember subscribes to tenant member administration change
	// events (create, role/scope changes, enable/disable, removal). It is
	// tenant-scoped and platform-admin only, matching the member REST
	// routes: the console re-fetches the tenant's member list on any frame.
	ResourceMember = "member"

	// ResourceAttribute subscribes to device attribute state changes
	// (CLIENT/SERVER_SCOPE/SHARED_SCOPE), narrowed per device by data scope
	// like telemetry and alarms.
	ResourceAttribute = "attribute"

	// ResourceShadow subscribes to device shadow changes (desired patches and
	// device-reported state), narrowed per device by data scope.
	ResourceShadow = "shadow"
)

// AlarmWildcard matches every alarm subject.
const AlarmWildcard = "nl.alarm.*.>"

// NotificationWildcard matches every in-app notification subject.
const NotificationWildcard = "nl.notification.*"

// CommandTaskWildcard matches batch command progress subjects.
const CommandTaskWildcard = "nl.command.task.>"

// ProbeTaskWildcard matches on-demand probe progress subjects.
const ProbeTaskWildcard = "nl.probe.task.>"

// OTATaskWildcard matches firmware OTA progress subjects.
const OTATaskWildcard = "nl.ota.task.>"

// PresenceWildcard matches every device presence subject.
const PresenceWildcard = "nl.presence.>"

// AttributeWildcard matches console-plane attribute change subjects
// (nl.attribute.event.<tenant>.<device>).
const AttributeWildcard = "nl.attribute.event.>"

// ShadowWildcard matches console-plane shadow change subjects
// (nl.shadow.event.<tenant>.<device>).
const ShadowWildcard = "nl.shadow.event.>"

// SecretEventWildcard matches secret delivery event subjects
// (nl.secret.event.<tenant>).
const SecretEventWildcard = "nl.secret.event.>"

// Error codes returned by the WebSocket endpoint. Codes follow the global
// upper-snake-case error vocabulary so clients can share handling with REST.
const (
	// ErrorCodeInvalidFrame indicates malformed control JSON.
	ErrorCodeInvalidFrame = "WS_INVALID_FRAME"

	// ErrorCodeUnsupportedSubscription indicates an unknown resource.
	ErrorCodeUnsupportedSubscription = "WS_UNSUPPORTED_SUBSCRIPTION"

	// ErrorCodeQuotaExceeded indicates a tenant quota blocked the frame.
	ErrorCodeQuotaExceeded = "QUOTA_EXCEEDED"
)
