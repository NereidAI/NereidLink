// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_get.go answers device-driven pulls of platform-managed
// attributes (SERVER_SCOPE/SHARED_SCOPE). A device or gateway publishes a
// small request naming the scopes (empty means both) and the platform writes
// the current key/value sets back on the matching ack topic, closing the
// offline gap: an edge that missed a v1/.../attributes/down push while
// disconnected recovers on demand after connect.

package ingestmqtt

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// writeToClient marshals payload and writes one PUBLISH to the live client,
// honoring a device-supplied response topic or falling back to defaultTopic.
func (h *PublishHook) writeToClient(clientID, responseTopic, defaultTopic string, payload any) {
	topic := responseTopic
	if topic == "" {
		topic = defaultTopic
	}
	client, ok := h.brokerClient(clientID)
	if !ok {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		h.Log.Warn("attribute ack encode failed", "client", clientID, "error", err)
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   topic,
		Payload:     raw,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		h.Log.Warn("attribute ack write failed", "client", clientID, "error", err)
	}
}

// handleDeviceAttributesGet answers a directly connected device's fetch.
func (h *PublishHook) handleDeviceAttributesGet(clientID string, payload []byte) {
	if h.attributeFetcher == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated attribute fetch reached publish hook", "client", clientID)
		return
	}
	var frame mqttapi.AttributesGetFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid device attribute fetch payload", "client", clientID, "error", err)
		return
	}
	identity := current.identity
	scopes, err := h.attributeFetcher.FetchAttributes(context.Background(), identity.TenantID, identity.DeviceID, frame.Scopes)
	if err != nil {
		h.Log.Warn("device attribute fetch failed", "client", clientID, "error", err)
		return
	}
	h.writeToClient(clientID, frame.ResponseTopic,
		mqttapi.TopicDeviceAttributesGetAck,
		mqttapi.AttributesGetAckFrame{Scopes: scopes})
}

// handleGatewayAttributesGet answers a gateway's fetch for one attached
// sub-device.
func (h *PublishHook) handleGatewayAttributesGet(clientID string, payload []byte) {
	if h.attributeFetcher == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway attribute fetch reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway attribute fetch", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway attribute fetch before hello; dropped", "client", clientID)
		return
	}
	var frame mqttapi.GatewayAttributesGetFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway attribute fetch payload", "client", clientID, "error", err)
		return
	}
	if frame.Device == "" || !h.connections.isSubdevice(clientID, identity.TenantID, frame.Device) {
		h.Log.Warn("gateway attribute fetch for un-attached sub-device dropped",
			"client", clientID, "device", frame.Device)
		return
	}
	scopes, err := h.attributeFetcher.FetchAttributes(context.Background(), identity.TenantID, frame.Device, frame.Scopes)
	if err != nil {
		h.Log.Warn("gateway attribute fetch failed", "client", clientID, "device", frame.Device, "error", err)
		return
	}
	h.writeToClient(clientID, frame.ResponseTopic,
		mqttapi.TopicGatewayAttributesGetAck,
		mqttapi.GatewayAttributesGetAckFrame{Device: frame.Device, Scopes: scopes})
}
