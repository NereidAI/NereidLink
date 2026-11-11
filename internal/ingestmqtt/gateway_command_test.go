// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gateway_command_test.go verifies gateway-relayed sub-device command
// dispatch routing/encoding and the gateway command/response uplink gate.
package ingestmqtt

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// sdkIdentity builds a direct or gateway-capability device identity.
func sdkIdentity(tenantID, deviceID string, gateway bool) sdktelemetry.DeviceIdentity {
	identity := sdktelemetry.DeviceIdentity{TenantID: tenantID, DeviceID: deviceID}
	if gateway {
		identity.Capabilities = []string{mqttapi.CapabilityGateway}
	}
	return identity
}

func TestResolveGatewayCommandAttached(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	wire := &v1.Command{
		Id:          "c1",
		TenantId:    "t",
		DeviceId:    "sub1",
		GatewayId:   "g",
		Name:        "reboot",
		Params:      []byte(`{"delay":2}`),
		Interaction: command.InteractionOneWay,
		Timeliness:  command.TimelinessRealtime,
	}
	clientID, payload, ack := relay.resolveGatewayCommand(wire)
	if ack != nil {
		t.Fatalf("expected delivery, got ack %+v", ack)
	}
	if clientID != "gw" {
		t.Fatalf("client = %q, want gw", clientID)
	}
	var frame gatewayCommandFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.CommandID != "c1" || frame.Device != "sub1" || frame.Method != "reboot" ||
		frame.Interaction != command.InteractionOneWay || string(frame.Params) != `{"delay":2}` {
		t.Fatalf("unexpected gateway frame: %+v", frame)
	}
}

func TestResolveGatewayCommandRejectsForeignSubdevice(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	wire := &v1.Command{
		Id: "c2", TenantId: "t", DeviceId: "ghost", GatewayId: "g",
		Name: "reboot", Interaction: command.InteractionOneWay,
	}
	_, _, ack := relay.resolveGatewayCommand(wire)
	if ack == nil || ack.Status != command.StatusNotConnected {
		t.Fatalf("unattached sub-device must be not-connected, got %+v", ack)
	}
}

func TestResolveGatewayCommandGatewayOffline(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	wire := &v1.Command{
		Id: "c3", TenantId: "t", DeviceId: "sub1", GatewayId: "offline-gw",
		Name: "reboot", Interaction: command.InteractionOneWay,
	}
	_, _, ack := relay.resolveGatewayCommand(wire)
	if ack == nil || ack.Status != command.StatusNotConnected {
		t.Fatalf("offline gateway must be not-connected, got %+v", ack)
	}
}

// recordedResponse captures a ReportResponse call.
type recordedResponse struct {
	tenantID  string
	deviceID  string
	commandID string
	payload   []byte
}

type fakeResponseReporter struct{ calls []recordedResponse }

func (f *fakeResponseReporter) ReportResponse(tenantID, deviceID, commandID string, payload []byte) {
	f.calls = append(f.calls, recordedResponse{tenantID, deviceID, commandID, payload})
}

func newGatewayResponseHook(t *testing.T) (*PublishHook, *fakeResponseReporter) {
	t.Helper()
	hooks := NewAuthHook(nil, nil, "ing")
	hooks.connections["gw"] = connection{
		identity:   sdkIdentity("t", "g", true),
		negotiated: true,
	}
	hooks.connections["gw-raw"] = connection{
		identity: sdkIdentity("t", "g2", true),
	}
	hooks.connections["dev"] = connection{identity: sdkIdentity("t", "d", false)}
	hooks.recordSubdevice("gw", "t", "sub1")
	reporter := &fakeResponseReporter{}
	hook := NewPublishHook(nil, hooks, nil, reporter, nil)
	hook.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return hook, reporter
}

func TestHandleGatewayCommandResponseCorrelates(t *testing.T) {
	hook, reporter := newGatewayResponseHook(t)
	hook.handleGatewayCommandResponse("gw",
		[]byte(`{"command_id":"c1","device":"sub1","data":{"ok":true}}`))
	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 correlation, got %d", len(reporter.calls))
	}
	got := reporter.calls[0]
	if got.tenantID != "t" || got.deviceID != "sub1" || got.commandID != "c1" ||
		string(got.payload) != `{"ok":true}` {
		t.Fatalf("unexpected correlation: %+v", got)
	}
}

func TestHandleGatewayCommandResponseGates(t *testing.T) {
	hook, reporter := newGatewayResponseHook(t)
	// Unattached sub-device.
	hook.handleGatewayCommandResponse("gw",
		[]byte(`{"command_id":"c","device":"other","data":{}}`))
	// Pre-hello gateway.
	hook.handleGatewayCommandResponse("gw-raw",
		[]byte(`{"command_id":"c","device":"sub1","data":{}}`))
	// Non-gateway identity.
	hook.handleGatewayCommandResponse("dev",
		[]byte(`{"command_id":"c","device":"sub1","data":{}}`))
	// Malformed.
	hook.handleGatewayCommandResponse("gw", []byte(`{bad`))
	// Missing device.
	hook.handleGatewayCommandResponse("gw",
		[]byte(`{"command_id":"c","data":{}}`))
	if len(reporter.calls) != 0 {
		t.Fatalf("gated frames must not correlate, got %+v", reporter.calls)
	}
}
