// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// otarel ay.go bridges Core NATS OTA dispatch requests with the owning
// gateway MQTT connection (ADR-0026). The platform groups every device of a
// firmware task that shares a gateway into one DispatchFrame; the relay writes
// it to the single live gateway client on v1/gateway/ota/task and the gateway
// reports per-device progress on v1/gateway/ota/progress. Progress is
// asynchronous: the relay does not block on a reply, it forwards each uplink
// frame as a Core NATS event the OTA manager correlates by task id and device.

package ingestmqtt

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/ota"
)

// OTARelay dispatches firmware frames to gateway clients and reports per-device
// progress back onto Core NATS.
type OTARelay struct {
	nc     *nats.Conn
	broker *mqtt.Server
	hooks  *AuthHook
}

// NewOTARelay constructs the ingress OTA relay.
func NewOTARelay(nc *nats.Conn, broker *mqtt.Server, hooks *AuthHook) *OTARelay {
	return &OTARelay{nc: nc, broker: broker, hooks: hooks}
}

// Subscribe starts listening for OTA dispatches addressed to this ingress.
func (r *OTARelay) Subscribe(ingressID string) error {
	_, err := r.nc.Subscribe(ota.DispatchSubjectPrefix+ingressID, r.handleDispatch)
	return err
}

// gatewayOTATaskFrame is the JSON body written on v1/gateway/ota/task. It
// mirrors ota.DispatchFrame with the edge field names from the protocol.
type gatewayOTATaskFrame struct {
	TaskID        string            `json:"task_id"`
	Devices       []string          `json:"devices"`
	Version       string            `json:"version"`
	URL           string            `json:"url"`
	SHA256        string            `json:"sha256"`
	Size          int64             `json:"size"`
	Headers       map[string]string `json:"headers,omitempty"`
	SuccessPolicy string            `json:"success_policy,omitempty"`
}

// handleDispatch resolves the owning gateway client and writes one firmware
// frame. A missing client is reported as a per-device failed event so the
// manager does not wait on an undeliverable dispatch.
func (r *OTARelay) handleDispatch(msg *nats.Msg) {
	var frame ota.DispatchFrame
	if json.Unmarshal(msg.Data, &frame) != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		r.failAll(frame, "gateway not connected")
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		r.failAll(frame, "gateway not connected")
		return
	}
	payload, err := json.Marshal(gatewayOTATaskFrame{
		TaskID:        frame.TaskID,
		Devices:       frame.Devices,
		Version:       frame.Version,
		URL:           frame.URL,
		SHA256:        frame.SHA256,
		Size:          frame.Size,
		Headers:       frame.Headers,
		SuccessPolicy: frame.SuccessPolicy,
	})
	if err != nil {
		r.failAll(frame, "frame encode failed")
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayOTATask,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("ota dispatch write failed", "gateway", frame.GatewayID, "error", err)
		r.failAll(frame, "transport write failed")
	}
}

// failAll emits one failed event per device so the task does not wait on a
// dispatch that could not be delivered.
func (r *OTARelay) failAll(frame ota.DispatchFrame, reason string) {
	for _, deviceID := range frame.Devices {
		r.publishEvent(ota.Event{
			TaskID:    frame.TaskID,
			TenantID:  frame.TenantID,
			GatewayID: frame.GatewayID,
			DeviceID:  deviceID,
			State:     ota.ItemStateFailed,
			Message:   reason,
		})
	}
}

// handleGatewayOTAProgress consumes a per-device firmware progress frame on
// v1/gateway/ota/progress. It gates gateway identity and negotiation, then
// forwards the frame to the OTA manager over Core NATS. Malformed frames are
// dropped.
func (h *PublishHook) handleGatewayOTAProgress(clientID string, payload []byte) {
	if h.otaResults == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	h.otaResults.ReportProgress(current.identity.TenantID, current.identity.DeviceID, payload)
}

// ReportProgress forwards a gateway uplink OTA frame as a Core NATS event.
// The gateway is trusted for the device id only after the publish hook gates
// the connection as a negotiated gateway. Edge states are upper-case in the
// protocol example; they are normalized to the lowercase internal states and
// SUCCESS is mapped to succeeded.
func (r *OTARelay) ReportProgress(tenantID, gatewayID string, payload []byte) {
	var uplink ota.ProgressFrame
	if json.Unmarshal(payload, &uplink) != nil || uplink.TaskID == "" || uplink.Device == "" {
		return
	}
	state := normalizeOTAState(uplink.State)
	if state == "" {
		return
	}
	r.publishEvent(ota.Event{
		TaskID:          uplink.TaskID,
		TenantID:        tenantID,
		GatewayID:       gatewayID,
		DeviceID:        uplink.Device,
		State:           state,
		ResultCode:      uplink.ResultCode,
		ProgressPct:     uplink.ProgressPct,
		Bytes:           uplink.Bytes,
		FirmwareVersion: uplink.FirmwareVersion,
		Message:         uplink.Message,
		Source:          ota.SourceGateway,
	})
}

// normalizeOTAState maps an edge-reported state to an internal item phase.
// Both lowercase and the documented upper-case spellings are accepted, and
// SUCCESS maps to succeeded.
func normalizeOTAState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ota.ItemStatePending, ota.ItemStateInitiated, ota.ItemStateDownloading,
		ota.ItemStateDownloaded, ota.ItemStateVerified, ota.ItemStateUpdating,
		ota.ItemStateUpdated, ota.ItemStateRebooting, ota.ItemStateConfirmed,
		ota.ItemStateFailed:
		return strings.ToLower(strings.TrimSpace(s))
	case "success", "succeeded":
		return ota.ItemStateSucceeded
	default:
		return ""
	}
}

// publishEvent emits one per-device progress event on the manager subject.
func (r *OTARelay) publishEvent(ev ota.Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := r.nc.Publish(ota.EventsSubjectPrefix+ev.TenantID+"."+ev.GatewayID, raw); err != nil {
		slog.Warn("ota event publish failed", "gateway", ev.GatewayID, "error", err)
	}
}
