// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_downlink_test.go verifies a server/shared attribute push routes
// to a direct device session or to the gateway that attached the sub-device,
// and that an unroutable/invalid frame is skipped rather than written.
package ingestmqtt

import (
	"encoding/json"
	"testing"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

func newAttributeRelayHooks() *AuthHook {
	hooks := NewAuthHook(nil, nil, "ing")
	hooks.connections["dev"] = connection{identity: sdktelemetry.DeviceIdentity{
		TenantID: "t", DeviceID: "d",
	}}
	hooks.devices[deviceKey("t", "d")] = "dev"
	hooks.connections["gw"] = connection{
		identity: sdktelemetry.DeviceIdentity{
			TenantID:     "t",
			DeviceID:     "g",
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
	}
	hooks.devices[deviceKey("t", "g")] = "gw"
	hooks.recordSubdevice("gw", "t", "sub1")
	return hooks
}

func TestResolveAttributePushDirectDevice(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	frame := mqttapi.AttributePushFrame{
		TenantID: "t", DeviceID: "d", Scope: "SHARED_SCOPE",
		Key: "k", Value: json.RawMessage(`"v"`),
	}
	clientID, topic, payload, ok := relay.resolveAttributePush(frame)
	if !ok || clientID != "dev" || topic != mqttapi.TopicDeviceAttributesDown {
		t.Fatalf("direct route wrong: client=%q topic=%q ok=%v", clientID, topic, ok)
	}
	var wire mqttapi.DeviceAttributesDownFrame
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("wire frame decode: %v", err)
	}
	if wire.Scope != "SHARED_SCOPE" || string(wire.Data["k"]) != `"v"` {
		t.Fatalf("unexpected direct wire frame: %+v", wire)
	}
}

func TestResolveAttributePushViaGateway(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	frame := mqttapi.AttributePushFrame{
		TenantID: "t", DeviceID: "sub1", Scope: "SHARED_SCOPE",
		Key: "k", Value: json.RawMessage(`1`),
	}
	clientID, topic, payload, ok := relay.resolveAttributePush(frame)
	if !ok || clientID != "gw" || topic != mqttapi.TopicGatewayAttributesPush {
		t.Fatalf("gateway route wrong: client=%q topic=%q ok=%v", clientID, topic, ok)
	}
	var wire mqttapi.GatewayAttributesPushFrame
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("wire frame decode: %v", err)
	}
	if wire.Device != "sub1" || wire.Scope != "SHARED_SCOPE" || string(wire.Data["k"]) != "1" {
		t.Fatalf("unexpected gateway wire frame: %+v", wire)
	}
}

func TestResolveAttributePushNoSession(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	if _, _, _, ok := relay.resolveAttributePush(mqttapi.AttributePushFrame{
		TenantID: "t", DeviceID: "ghost", Scope: "SHARED_SCOPE",
		Key: "k", Value: json.RawMessage(`1`),
	}); ok {
		t.Fatal("a device with no live session must not route")
	}
	for _, bad := range []mqttapi.AttributePushFrame{
		{DeviceID: "d", Key: "k"},
		{TenantID: "t", Key: "k"},
		{TenantID: "t", DeviceID: "d"},
	} {
		if _, _, _, ok := relay.resolveAttributePush(bad); ok {
			t.Fatalf("malformed frame must not route: %+v", bad)
		}
	}
}
