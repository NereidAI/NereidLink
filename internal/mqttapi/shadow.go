// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow.go defines the MQTT wire frames for device shadow on both the
// direct (v1/me) and gateway-proxied (v1/gateway) transports. These are the
// device-facing payloads only; the Core NATS frame that carries a desired
// change from the shadow service to an ingress lives in internal/shadow.
package mqttapi

import "encoding/json"

// DeviceShadowDesiredFrame is the clean payload written on
// v1/me/shadow/desired for a directly connected device (including a gateway
// acting on its own behalf). It carries no internal tenant/device fields:
// the device already knows its own identity from the connection.
type DeviceShadowDesiredFrame struct {
	// Version is the monotonically increasing desired version.
	Version int64 `json:"version"`

	// Desired is the complete desired state (not a delta). The device
	// replaces its local desired map wholesale.
	Desired map[string]any `json:"desired"`
}

// GatewayShadowReport is one sub-device reported-state entry inside a
// gateway batch report.
type GatewayShadowReport struct {
	// Device names the attached sub-device the report belongs to.
	Device string `json:"device"`

	// State is the reported patch merged server-side per top-level key. A
	// JSON null value deletes that key. It is kept raw so one entry with a
	// non-object state can be rejected without failing the whole batch.
	State json.RawMessage `json:"state"`

	// Version optionally carries the desired version the sub-device
	// applied; omitted means the current desired version.
	Version *int64 `json:"version,omitempty"`
}

// GatewayShadowReportFrame is the payload published on
// v1/gateway/shadow/report: a batch of one or more sub-device reports. A
// single-device report is simply a length-one batch.
type GatewayShadowReportFrame struct {
	Reports []GatewayShadowReport `json:"reports"`
}

// GatewayShadowDesired is one sub-device full desired-state entry inside a
// gateway batch push.
type GatewayShadowDesired struct {
	// Device names the target sub-device behind the gateway.
	Device string `json:"device"`

	// Version is the desired version for this sub-device.
	Version int64 `json:"version"`

	// Desired is the complete desired state (not a delta).
	Desired map[string]any `json:"desired"`
}

// GatewayShadowDesiredFrame is the payload written on
// v1/gateway/shadow/desired: a batch of one or more sub-device desired
// states. One gateway subscription serves every attached device; the
// gateway demuxes each entry by Device.
type GatewayShadowDesiredFrame struct {
	Desired []GatewayShadowDesired `json:"desired"`
}
