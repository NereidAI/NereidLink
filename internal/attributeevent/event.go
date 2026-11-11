// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package attributeevent fans device attribute state changes out over Core
// NATS to the WebSocket plane. Unlike mqttapi's nl.attribute.push root (which
// drives the ingress-facing device downlink), this root is a console-plane
// change notification: every successful attribute upsert — platform REST
// writes (SERVER_SCOPE/SHARED_SCOPE) and device/rule writes (CLIENT scope) —
// emits one frame per changed key so subscribed operator UIs update live.
//
// The hub wildcard-subscribes and narrows delivery by the subscriber's data
// scope; this package itself never branches on the value. Frames are
// best-effort and fire after the database commit, so a missed delivery is
// harmless (the client re-reads on reload).
package attributeevent

import (
	"encoding/json"
	"time"
)

// SubjectPrefix is the console-plane attribute-event subject root. Frames are
// published on "<prefix>.<tenant>.<device>"; token layout:
// nl(1).attribute(2).event(3).<tenant>(4).<device>(5).
const SubjectPrefix = "nl.attribute.event"

// Wildcard matches every attribute-event subject.
const Wildcard = "nl.attribute.event.>"

// Event is one changed attribute key. Only the changed key is carried; Value
// is the raw JSON the platform stored and UpdatedAt is the storage timestamp.
type Event struct {
	// TenantID owns the device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the target device (a gateway-attached sub-device uses its
	// own device id; the hub matches subscriptions on it).
	DeviceID string `json:"device_id"`

	// Scope is CLIENT, SERVER_SCOPE or SHARED_SCOPE.
	Scope string `json:"scope"`

	// Key is the changed attribute key.
	Key string `json:"key"`

	// Value is the raw JSON value after the upsert.
	Value json.RawMessage `json:"value"`

	// UpdatedAt is the storage timestamp (RFC3339 in JSON).
	UpdatedAt time.Time `json:"updated_at"`

	// Source records the writer: "server" (platform REST) or "device"
	// (device uplink or a rule-engine Save Attributes node).
	Source string `json:"source"`
}

// Source values.
const (
	// SourceServer is a platform/operator write through the REST API.
	SourceServer = "server"
	// SourceDevice is a device-reported or rule-engine produced write.
	SourceDevice = "device"
)
