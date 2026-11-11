// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_uplink.go handles gateway-proxied sub-device shadow reports. One
// MQTT frame may carry several sub-device reports; each entry is authorized
// and persisted independently so one bad or unattached entry never rejects
// the rest of the batch. Rejected entries are recorded in the audit log.

package ingestmqtt

import (
	"context"
	"encoding/json"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/shadow"
)

// shadowReportAuditor records rejected gateway shadow entries. It is
// satisfied by *audit.Service; nil disables the audit trail.
type shadowReportAuditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// handleGatewayShadowReport persists a batch of sub-device reported-state
// patches published on v1/gateway/shadow/report.
func (h *PublishHook) handleGatewayShadowReport(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway shadow report reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway shadow report", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway shadow report before hello; dropped", "client", clientID)
		return
	}

	var frame mqttapi.GatewayShadowReportFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		// The top-level payload cannot be parsed: per-entry isolation is
		// impossible, so the whole frame is dropped.
		h.Log.Warn("invalid gateway shadow report payload", "client", clientID, "error", err)
		return
	}
	if h.shadows == nil {
		return
	}

	for _, entry := range frame.Reports {
		h.applyShadowReportEntry(clientID, identity.TenantID, identity.DeviceID, entry)
	}
}

// applyShadowReportEntry validates and stores one sub-device report. A reject
// is logged and audited but never aborts the surrounding batch.
func (h *PublishHook) applyShadowReportEntry(clientID, tenantID, gatewayID string, entry mqttapi.GatewayShadowReport) {
	if entry.Device == "" {
		h.rejectShadowEntry(clientID, tenantID, gatewayID, "", "empty device", nil)
		return
	}
	if !h.connections.isSubdevice(clientID, tenantID, entry.Device) {
		h.rejectShadowEntry(clientID, tenantID, gatewayID, entry.Device, "sub-device not attached to gateway", nil)
		return
	}

	var state map[string]any
	if err := json.Unmarshal(entry.State, &state); err != nil || state == nil {
		h.rejectShadowEntry(clientID, tenantID, gatewayID, entry.Device, "state must be a JSON object", map[string]any{
			"parse_error": errString(err),
		})
		return
	}
	if len(state) == 0 {
		h.rejectShadowEntry(clientID, tenantID, gatewayID, entry.Device, "state must be a non-empty JSON object", nil)
		return
	}

	patch := shadow.ReportedPatch{State: state, Version: entry.Version}
	if _, err := h.shadows.UpdateReported(context.Background(), entry.Device, patch); err != nil {
		h.Log.Warn("gateway sub-device shadow report failed",
			"client", clientID, "device", entry.Device, "error", err)
	}
}

// rejectShadowEntry writes one structured warn line and an audit record for a
// dropped report. Audit failure never affects ingest.
func (h *PublishHook) rejectShadowEntry(clientID, tenantID, gatewayID, deviceID, reason string, extra map[string]any) {
	h.Log.Warn("gateway shadow report entry rejected",
		"client", clientID, "gateway", gatewayID, "device", deviceID, "reason", reason)
	if h.shadowAuditor == nil {
		return
	}
	details := map[string]any{
		"gateway_client": clientID,
		"gateway":        gatewayID,
		"reason":         reason,
	}
	for key, value := range extra {
		details[key] = value
	}
	if _, err := h.shadowAuditor.Append(context.Background(), audit.Input{
		TenantID:     tenantID,
		EventType:    audit.EventConfigChange,
		Action:       "shadow-report-rejected",
		ResourceType: "device_shadow",
		ResourceID:   deviceID,
		Details:      details,
	}); err != nil {
		h.Log.Warn("failed to audit rejected shadow entry", "device", deviceID, "error", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
