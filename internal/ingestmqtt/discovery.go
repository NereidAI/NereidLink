// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discovery.go implements the MQTT side of gateway auto-provisioning
// (ADR-0026 / ADR-0013): it gates a published discovery frame on an
// authenticated, negotiated gateway, hands the transport-independent frame to
// the discovery service, and writes the local_id -> device_id mapping back to
// that one client. Tenant and gateway always come from the connection
// identity, never from the frame body, so a gateway can only provision
// sub-devices for itself.

package ingestmqtt

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/nereidai/nereidlink/internal/discovery"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// handleGatewayDiscovery consumes one frame on v1/gateway/discovery and
// publishes the per-candidate result on v1/gateway/discovery/result to the
// requesting client. Like other gateway uplinks it is fire-and-forget at the
// transport level: malformed payloads are logged and dropped, but a
// successfully processed frame always gets a result so the edge can correlate
// its local ids to platform UUIDs.
func (h *PublishHook) handleGatewayDiscovery(clientID string, payload []byte) {
	if h.discovery == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}

	var frame discovery.Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway discovery payload", "client", clientID, "error", err)
		return
	}

	result, err := h.discovery.HandleDiscovery(context.Background(),
		current.identity.TenantID, current.identity.DeviceID, frame)
	if err != nil {
		h.Log.Warn("gateway discovery handling failed",
			"client", clientID, "error", err)
		// Echo an empty result so the edge does not wait indefinitely; the
		// per-frame failure (e.g. oversize batch, bad default profile) is an
		// operator/deployment condition rather than a candidate condition.
		result = discovery.Result{Results: []discovery.ItemResult{}}
	}
	h.publishDiscoveryResult(clientID, result)
}

// publishDiscoveryResult writes the provisioning result to one client on the
// fixed downlink topic. The edge is required to have subscribed during hello
// (the topic is in the gateway subscription allowlist); a missing client or a
// failed write is logged, not retried, because the edge can simply re-send a
// discovery frame and provisioning is idempotent.
func (h *PublishHook) publishDiscoveryResult(clientID string, result discovery.Result) {
	client, ok := h.brokerClient(clientID)
	if !ok {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		h.Log.Warn("discovery result encode failed", "client", clientID, "error", err)
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayDiscoveryResult,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		h.Log.Warn("discovery result write failed", "client", clientID, "error", err)
	}
}
