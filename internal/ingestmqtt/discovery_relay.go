// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discoveryrelay.go delivers asynchronous human-review approval results to
// the owning gateway (ADR-0026, migration 0046). The discovery service
// publishes a DecisionFrame on nl.discovery.dispatch.<ingress> when an
// operator approves or rejects a queued candidate; this relay resolves the
// live gateway client and writes the same discovery/result frame the
// synchronous uplink path emits, so the edge correlation code path is
// identical for immediate and reviewed provisioning.

package ingestmqtt

import (
	"encoding/json"
	"log/slog"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/discovery"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// DiscoveryRelay routes approval-result frames from Core NATS to one gateway
// MQTT client.
type DiscoveryRelay struct {
	nc     *nats.Conn
	broker *mqtt.Server
	hooks  *AuthHook
}

// NewDiscoveryRelay constructs the discovery decision relay.
func NewDiscoveryRelay(nc *nats.Conn, broker *mqtt.Server, hooks *AuthHook) *DiscoveryRelay {
	return &DiscoveryRelay{nc: nc, broker: broker, hooks: hooks}
}

// Subscribe listens for discovery decisions addressed to this ingress.
func (r *DiscoveryRelay) Subscribe(ingressID string) error {
	_, err := r.nc.Subscribe(discovery.DecisionDispatchSubjectPrefix+ingressID, r.handleDecision)
	return err
}

// handleDecision resolves the gateway client and writes one result frame. A
// missing client means the gateway is offline; the outcome is already durable
// and its next discovery frame resolves as "existing", so the message is
// dropped rather than failed.
func (r *DiscoveryRelay) handleDecision(msg *nats.Msg) {
	var frame discovery.DecisionFrame
	if json.Unmarshal(msg.Data, &frame) != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	payload, err := json.Marshal(frame.Result)
	if err != nil {
		slog.Warn("discovery decision encode failed", "gateway", frame.GatewayID, "error", err)
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
		slog.Warn("discovery decision write failed", "gateway", frame.GatewayID, "error", err)
	}
}
