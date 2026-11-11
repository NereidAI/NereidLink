// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_uplink_test.go verifies direct and gateway attribute uplinks
// persist to CLIENT scope with the correct identity and sub-device gating.
package ingestmqtt

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// savedAttribute is one captured AttributeSaver invocation.
type savedAttribute struct {
	tenantID string
	deviceID string
	scope    string
	attrs    map[string]any
}

// fakeAttributeSaver records upserts instead of touching a database.
type fakeAttributeSaver struct {
	calls []savedAttribute
}

func (s *fakeAttributeSaver) SaveAttributes(_ context.Context, tenantID, deviceID, scope string, attrs map[string]any) error {
	s.calls = append(s.calls, savedAttribute{tenantID, deviceID, scope, attrs})
	return nil
}

// newAttributeTestHook builds a publish hook backed by an in-memory auth hook
// with one direct device and one negotiated gateway that attached sub1.
func newAttributeTestHook(t *testing.T) (*PublishHook, *fakeAttributeSaver) {
	t.Helper()
	hooks := NewAuthHook(nil, nil, "ing")
	hooks.connections["dev"] = connection{identity: sdktelemetry.DeviceIdentity{
		TenantID: "t", DeviceID: "d",
	}}
	hooks.connections["gw"] = connection{
		identity: sdktelemetry.DeviceIdentity{
			TenantID: "t", DeviceID: "g",
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
		negotiated: true,
	}
	hooks.connections["gw-raw"] = connection{
		identity: sdktelemetry.DeviceIdentity{
			TenantID:     "t",
			DeviceID:     "g2",
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
		negotiated: false,
	}
	hooks.recordSubdevice("gw", "t", "sub1")

	hook := NewPublishHook(nil, hooks, nil, nil, nil)
	hook.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	saver := &fakeAttributeSaver{}
	hook.SetAttributeSaver(saver)
	return hook, saver
}

func TestHandleDeviceAttributesPersistsClientScope(t *testing.T) {
	hook, saver := newAttributeTestHook(t)
	hook.handleDeviceAttributes("dev", []byte(`{"firmware":"1.0","retries":3}`))

	if len(saver.calls) != 1 {
		t.Fatalf("expected 1 save, got %d", len(saver.calls))
	}
	got := saver.calls[0]
	if got.tenantID != "t" || got.deviceID != "d" || got.scope != "CLIENT" {
		t.Fatalf("unexpected identity/scope: %+v", got)
	}
	if got.attrs["firmware"] != "1.0" || got.attrs["retries"] != float64(3) {
		t.Fatalf("unexpected attributes: %+v", got.attrs)
	}
}

func TestHandleDeviceAttributesRejectsMalformedFrame(t *testing.T) {
	hook, saver := newAttributeTestHook(t)
	hook.handleDeviceAttributes("dev", []byte(`{not json`))
	if len(saver.calls) != 0 {
		t.Fatalf("malformed frame must not be persisted, got %d calls", len(saver.calls))
	}
}

func TestHandleGatewayAttributesPersistsAttachedSubdevice(t *testing.T) {
	hook, saver := newAttributeTestHook(t)
	hook.handleGatewayAttributes("gw", []byte(`{"sub1":{"region":"east"}}`))

	if len(saver.calls) != 1 {
		t.Fatalf("expected 1 save, got %d", len(saver.calls))
	}
	got := saver.calls[0]
	if got.deviceID != "sub1" || got.scope != "CLIENT" || got.attrs["region"] != "east" {
		t.Fatalf("unexpected gateway attribute save: %+v", got)
	}
}

func TestHandleGatewayAttributesSkipsUnattachedSubdevice(t *testing.T) {
	hook, saver := newAttributeTestHook(t)
	// sub2 was never attached and must be dropped; sub1 still persists.
	hook.handleGatewayAttributes("gw", []byte(`{"sub2":{"a":1},"sub1":{"b":2}}`))
	if len(saver.calls) != 1 || saver.calls[0].deviceID != "sub1" {
		t.Fatalf("only the attached sub-device may persist, got %+v", saver.calls)
	}
}

func TestHandleGatewayAttributesRejectsNonGatewayAndPreHello(t *testing.T) {
	hook, saver := newAttributeTestHook(t)
	// A plain device identity must not write gateway attributes.
	hook.handleGatewayAttributes("dev", []byte(`{"sub1":{"a":1}}`))
	// A gateway that has not completed hello is rejected.
	hook.handleGatewayAttributes("gw-raw", []byte(`{"sub1":{"a":1}}`))
	if len(saver.calls) != 0 {
		t.Fatalf("expected no saves, got %+v", saver.calls)
	}
}

func TestGatewayReverseIndexResolvesOwner(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	hooks := hook.connections.(*AuthHook)
	if owner, ok := hooks.gatewayClientIDFor("t", "sub1"); !ok || owner != "gw" {
		t.Fatalf("sub1 should resolve to gateway gw, got %q ok=%v", owner, ok)
	}
	if _, ok := hooks.gatewayClientIDFor("t", "unknown"); ok {
		t.Fatal("unattached device must not resolve to a gateway")
	}
	// Dropping the sub-device clears the reverse entry.
	hooks.dropSubdevice("gw", "t", "sub1")
	if _, ok := hooks.gatewayClientIDFor("t", "sub1"); ok {
		t.Fatal("reverse index must be cleared on drop")
	}
}
