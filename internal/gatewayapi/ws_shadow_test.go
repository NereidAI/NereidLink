// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ws_shadow_test.go proves the shadow real-time resource is routed per
// device and isolated by tenant exactly like the other device-scoped streams,
// without a live socket or NATS. It reuses the test client helpers defined in
// ws_attribute_test.go.
package gatewayapi

import "testing"

// fullShadowFrame is a realistic console shadow event (full post-commit
// shadow plus tenant and source), used by the routing tests below.
const fullShadowFrame = `{"device_id":"dev-1","desired":{"k":"v"},` +
	`"reported":{},"delta":{"k":"v"},"desired_version":1,` +
	`"reported_version":0,"etag":"\"1\"","in_sync":false,` +
	`"updated_at":"2026-10-06T00:00:00Z","tenant_id":"tenant-A",` +
	`"source":"server"}`

func TestRouteShadowDeliversToSubscribedDevice(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant, dev = "tenant-A", "dev-1"
	client := newTestClient()
	client.subscribe("sub1", ResourceShadow, tenant, []string{dev}, false)
	hub.clients[client] = struct{}{}

	hub.routeShadow(tenant, []byte(fullShadowFrame))

	frame := drainFrame(t, client)
	if frame.Type != ResourceShadow {
		t.Fatalf("frame type=%q want shadow", frame.Type)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %#v", frame.Data)
	}
	if data["tenant_id"] != "tenant-A" || data["source"] != "server" {
		t.Fatalf("payload lost identity fields: %#v", data)
	}
	if desired, _ := data["desired"].(map[string]any); desired["k"] != "v" {
		t.Fatalf("desired not forwarded: %#v", data["desired"])
	}

	// A frame for another device of the same tenant must not be delivered.
	hub.routeShadow(tenant, []byte(
		`{"device_id":"dev-other","desired":{},"tenant_id":"tenant-A"}`))
	assertNoFrame(t, client)

	// A frame for another tenant must not be delivered either.
	hub.routeShadow("tenant-B", []byte(
		`{"device_id":"dev-1","desired":{},"tenant_id":"tenant-B"}`))
	assertNoFrame(t, client)
}

func TestRouteShadowWildcardDataScope(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceShadow, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeShadow(tenant, []byte(
		`{"device_id":"dev-9","desired":{},"tenant_id":"tenant-A"}`))
	frame := drainFrame(t, client)
	if frame.Type != ResourceShadow {
		t.Fatalf("frame type=%q want shadow", frame.Type)
	}
}

func TestRouteShadowDropsMalformedFrame(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceShadow, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeShadow(tenant, []byte(`{not json`))
	assertNoFrame(t, client)
}
