// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_relay_test.go verifies a desired-state push routes to a direct
// session as the clean v1/me frame, or to the attaching gateway as a
// length-one v1/gateway batch, with no internal fields leaking either way.
package ingestmqtt

import (
	"encoding/json"
	"testing"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/shadow"
)

func TestResolveShadowDesiredDirectFrame(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	frame := shadow.PushFrame{
		TenantID: "t", DeviceID: "d", Version: 4,
		State: map[string]any{"led": "on"},
	}
	clientID, topic, payload, ok := relay.resolveShadowDesired(frame)
	if !ok || clientID != "dev" || topic != mqttapi.TopicDeviceShadowDesired {
		t.Fatalf("direct route wrong: client=%q topic=%q ok=%v", clientID, topic, ok)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, leak := wire["tenant_id"]; leak {
		t.Fatal("direct frame must not leak tenant_id")
	}
	if _, leak := wire["device_id"]; leak {
		t.Fatal("direct frame must not leak device_id")
	}
	if _, hasState := wire["state"]; hasState {
		t.Fatal("direct frame must name the field desired, not state")
	}
	var clean mqttapi.DeviceShadowDesiredFrame
	if err := json.Unmarshal(payload, &clean); err != nil {
		t.Fatalf("clean frame decode: %v", err)
	}
	if clean.Version != 4 || clean.Desired["led"] != "on" {
		t.Fatalf("unexpected clean frame: %+v", clean)
	}
}

func TestResolveShadowDesiredViaGateway(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	frame := shadow.PushFrame{
		TenantID: "t", DeviceID: "sub1", Version: 7,
		State: map[string]any{"rpm": float64(1200)},
	}
	clientID, topic, payload, ok := relay.resolveShadowDesired(frame)
	if !ok || clientID != "gw" || topic != mqttapi.TopicGatewayShadowDesired {
		t.Fatalf("gateway route wrong: client=%q topic=%q ok=%v", clientID, topic, ok)
	}
	var batch mqttapi.GatewayShadowDesiredFrame
	if err := json.Unmarshal(payload, &batch); err != nil {
		t.Fatalf("batch decode: %v", err)
	}
	if len(batch.Desired) != 1 {
		t.Fatalf("expected a length-one batch, got %d", len(batch.Desired))
	}
	entry := batch.Desired[0]
	if entry.Device != "sub1" || entry.Version != 7 || entry.Desired["rpm"] != float64(1200) {
		t.Fatalf("unexpected batch entry: %+v", entry)
	}
}

func TestResolveShadowDesiredNoSession(t *testing.T) {
	relay := NewCommandRelay(nil, nil, newAttributeRelayHooks())
	if _, _, _, ok := relay.resolveShadowDesired(shadow.PushFrame{
		TenantID: "t", DeviceID: "ghost", Version: 1,
	}); ok {
		t.Fatal("a device with no live session must not route")
	}
	for _, bad := range []shadow.PushFrame{
		{DeviceID: "d"},
		{TenantID: "t"},
	} {
		if _, _, _, ok := relay.resolveShadowDesired(bad); ok {
			t.Fatalf("malformed frame must not route: %+v", bad)
		}
	}
}
