// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes.go defines the Core NATS contract for server/shared attribute
// push from the management plane to connected devices. It lives beside the
// MQTT topic constants so the publisher (registry) and the last-mile relay
// (MQTT ingress) share one frame definition without the ingress importing the
// registry package.
package mqttapi

import (
	"encoding/json"
	"time"
)

// SubjectAttributePushPrefix is the ingress-facing subject root for
// server/shared attribute pushes. Messages are published on
// "<prefix>.<tenantID>.<deviceID>"; every MQTT ingress subscribes to the
// wildcard and only the node owning the live session actually writes a packet.
const SubjectAttributePushPrefix = "nl.attribute.push"

// AttributePushFrame is one server/shared attribute change addressed to one
// device. Only the changed key is carried; the value is the raw JSON the
// platform stored. Delivery is best-effort and idempotent: an offline device
// misses the push and reads current attributes on its next handshake/fetch.
type AttributePushFrame struct {
	// TenantID owns the target device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the target device; for a gateway-attached sub-device the
	// relay resolves the owning gateway session from this.
	DeviceID string `json:"device_id"`

	// Scope is SERVER_SCOPE or SHARED_SCOPE. Devices never receive CLIENT
	// scope pushes because they own that scope.
	Scope string `json:"scope"`

	// Key is the changed attribute key.
	Key string `json:"key"`

	// Value is the raw JSON value after the upsert.
	Value []byte `json:"value"`

	// UpdatedAt is the storage timestamp (RFC3339 UTC).
	UpdatedAt time.Time `json:"updated_at"`
}

// DeviceAttributesDownFrame is the payload written on v1/me/attributes/down
// for a directly connected device.
type DeviceAttributesDownFrame struct {
	// Scope is SERVER_SCOPE or SHARED_SCOPE.
	Scope string `json:"scope"`

	// Data carries the changed key/value pair. A JSON null value means the
	// key was deleted: the edge must drop it from its local cache.
	Data map[string]json.RawMessage `json:"data"`
}

// GatewayAttributesPushFrame is the payload written on
// v1/gateway/attributes/push for a gateway-attached sub-device (protocol
// v1.md: device, data).
type GatewayAttributesPushFrame struct {
	// Device names the target sub-device behind the gateway.
	Device string `json:"device"`

	// Scope is SERVER_SCOPE or SHARED_SCOPE.
	Scope string `json:"scope"`

	// Data carries the changed key/value pair. A JSON null value means the
	// key was deleted: the edge must remove it from its local cache.
	Data map[string]json.RawMessage `json:"data"`
}

// AttributesGetFrame is the uplink request a directly connected device
// publishes on v1/me/attributes/get to pull the platform-managed attributes.
// Only SERVER_SCOPE and SHARED_SCOPE are returned (the device owns CLIENT).
// An empty Scopes list means both platform scopes. ResponseTopic optionally
// overrides the v1/me/attributes/get/ack reply topic.
type AttributesGetFrame struct {
	// Scopes lists SERVER_SCOPE/SHARED_SCOPE; empty means both.
	Scopes []string `json:"scopes,omitempty"`

	// ResponseTopic overrides the built-in ack topic when non-empty.
	ResponseTopic string `json:"response_topic,omitempty"`
}

// GatewayAttributesGetFrame is the gateway-proxied fetch for one attached
// sub-device, published on v1/gateway/attributes/get.
type GatewayAttributesGetFrame struct {
	// Device names the attached sub-device to fetch for.
	Device string `json:"device"`

	// Scopes lists SERVER_SCOPE/SHARED_SCOPE; empty means both.
	Scopes []string `json:"scopes,omitempty"`

	// ResponseTopic overrides the built-in ack topic when non-empty.
	ResponseTopic string `json:"response_topic,omitempty"`
}

// AttributesGetAckFrame is the reply on v1/me/attributes/get/ack: one
// key/value map per requested scope.
type AttributesGetAckFrame struct {
	// Scopes maps each requested scope to its key/value pairs.
	Scopes map[string]map[string]json.RawMessage `json:"scopes"`
}

// GatewayAttributesGetAckFrame is the reply on
// v1/gateway/attributes/get/ack, naming the sub-device.
type GatewayAttributesGetAckFrame struct {
	// Device is the sub-device the attributes belong to.
	Device string `json:"device"`

	// Scopes maps each requested scope to its key/value pairs.
	Scopes map[string]map[string]json.RawMessage `json:"scopes"`
}
