// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// proberelay.go bridges Core NATS probe dispatch requests with the owning
// gateway MQTT connection (ADR-0026 section 9). The platform groups every
// device of a probe task that shares a gateway into one DispatchFrame; the
// relay writes that frame to the single live gateway client on
// v1/gateway/probe and the gateway answers per device on
// v1/gateway/probe/response. Results are asynchronous: unlike commands the
// relay does not block on a reply, it forwards each uplink result as a Core
// NATS event that the probe manager correlates by probe_id and device.

package ingestmqtt

import (
	"encoding/json"
	"log/slog"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/probe"
)

// ProbeRelay dispatches aggregated probe frames to gateway clients and
// reports per-device results back onto Core NATS.
type ProbeRelay struct {
	nc     *nats.Conn
	broker *mqtt.Server
	hooks  *AuthHook
}

// NewProbeRelay constructs the ingress probe relay.
func NewProbeRelay(nc *nats.Conn, broker *mqtt.Server, hooks *AuthHook) *ProbeRelay {
	return &ProbeRelay{nc: nc, broker: broker, hooks: hooks}
}

// Subscribe starts listening for probe dispatches addressed to this ingress.
func (r *ProbeRelay) Subscribe(ingressID string) error {
	_, err := r.nc.Subscribe(probe.DispatchSubjectPrefix+ingressID, r.handleDispatch)
	return err
}

// gatewayProbeFrame is the JSON body written on v1/gateway/probe. It mirrors
// probe.DispatchFrame with the edge field names from the protocol contract.
type gatewayProbeFrame struct {
	ProbeID   string   `json:"probe_id"`
	Devices   []string `json:"devices"`
	Mode      string   `json:"mode"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// handleDispatch resolves the owning gateway client and writes one frame. A
// missing client is a dispatch-time condition the manager also pre-checks via
// presence; the relay reports it as a per-device not-connected event so a
// race between the check and the write still terminalizes every item.
func (r *ProbeRelay) handleDispatch(msg *nats.Msg) {
	var frame probe.DispatchFrame
	if json.Unmarshal(msg.Data, &frame) != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		r.failAll(frame, probe.OutcomeNotConnected, "gateway not connected")
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		r.failAll(frame, probe.OutcomeNotConnected, "gateway not connected")
		return
	}
	payload, err := json.Marshal(gatewayProbeFrame{
		ProbeID:   frame.ProbeID,
		Devices:   frame.Devices,
		Mode:      frame.Mode,
		TimeoutMS: frame.TimeoutMS,
	})
	if err != nil {
		r.failAll(frame, probe.OutcomeTransportError, "frame encode failed")
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayProbe,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("probe dispatch write failed", "gateway", frame.GatewayID, "error", err)
		r.failAll(frame, probe.OutcomeTransportError, "transport write failed")
	}
}

// failAll emits one not-connected/transport event per device so the task does
// not wait on a dispatch that could not be delivered.
func (r *ProbeRelay) failAll(frame probe.DispatchFrame, outcome, reason string) {
	for _, deviceID := range frame.Devices {
		r.publishEvent(probe.Event{
			ProbeID:   frame.ProbeID,
			TenantID:  frame.TenantID,
			GatewayID: frame.GatewayID,
			DeviceID:  deviceID,
			Outcome:   outcome,
			Error:     reason,
		})
	}
}

// handleGatewayProbeResponse consumes a per-device probe result on
// v1/gateway/probe/response. It gates gateway identity and negotiation, then
// forwards the opaque result to the probe manager over Core NATS. The topic
// is fire-and-forget; malformed frames are dropped.
func (h *PublishHook) handleGatewayProbeResponse(clientID string, payload []byte) {
	if h.probeResults == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	h.probeResults.ReportResult(current.identity.TenantID, current.identity.DeviceID, payload)
}

// ReportResult forwards a gateway uplink probe result as a Core NATS event.
// The gateway is trusted for the device id only after the publish hook gates
// the connection as a negotiated gateway.
func (r *ProbeRelay) ReportResult(tenantID, gatewayID string, payload []byte) {
	for _, uplink := range decodeProbeResponses(payload) {
		outcome := probe.OutcomeOK
		if uplink.Error != "" {
			outcome = probe.OutcomeTimeout
		}
		r.publishEvent(probe.Event{
			ProbeID:   uplink.ProbeID,
			TenantID:  tenantID,
			GatewayID: gatewayID,
			DeviceID:  uplink.Device,
			Outcome:   outcome,
			Result:    uplink.Result,
			Error:     uplink.Error,
		})
	}
}

// decodeProbeResponses accepts both uplink forms on
// v1/gateway/probe/response: the legacy single-device frame
// {probe_id,device,result} and the optional batched frame
// {probe_id,responses:[...]}. Entries missing a probe id or device are
// dropped; a batched entry overrides its probe id with the frame-level id when
// the entry omits one, so a gateway need not repeat it per device.
func decodeProbeResponses(payload []byte) []probe.ResponseFrame {
	var bulk probe.BulkResponseFrame
	if err := json.Unmarshal(payload, &bulk); err == nil && bulk.ProbeID != "" && len(bulk.Responses) > 0 {
		out := make([]probe.ResponseFrame, 0, len(bulk.Responses))
		for _, e := range bulk.Responses {
			if e.Device == "" {
				continue
			}
			if e.ProbeID == "" {
				e.ProbeID = bulk.ProbeID
			}
			if e.ProbeID == "" {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	var single probe.ResponseFrame
	if json.Unmarshal(payload, &single) != nil || single.ProbeID == "" || single.Device == "" {
		return nil
	}
	return []probe.ResponseFrame{single}
}

// publishEvent emits one per-device outcome on the manager event subject.
func (r *ProbeRelay) publishEvent(ev probe.Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := r.nc.Publish(probe.EventsSubjectPrefix+ev.TenantID+"."+ev.DeviceID, raw); err != nil {
		slog.Warn("probe event publish failed", "device", ev.DeviceID, "error", err)
	}
}
