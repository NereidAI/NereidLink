// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ws_member_test.go proves the member real-time resource is routed as a
// tenant-wide, change-notification stream (like connector/secret events)
// and stays isolated per tenant/resource, without a live socket or NATS.
package gatewayapi

import "testing"

func TestRouteMemberDeliversToTenantSubscriber(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceMember, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeMember(tenant, []byte(
		`{"kind":"roles","tenant_id":"tenant-A","user_id":"user-1",`+
			`"at":"2026-10-14T00:00:00Z"}`))

	frame := drainFrame(t, client)
	if frame.Type != ResourceMember {
		t.Fatalf("frame type=%q want member", frame.Type)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok || data["kind"] != "roles" || data["user_id"] != "user-1" {
		t.Fatalf("unexpected payload: %#v", frame.Data)
	}
}

func TestRouteMemberTenantIsolation(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	client := newTestClient()
	client.subscribe("sub1", ResourceMember, "tenant-A", nil, true)
	hub.clients[client] = struct{}{}

	// A frame for another tenant must not be delivered.
	hub.routeMember("tenant-B", []byte(
		`{"kind":"created","tenant_id":"tenant-B","user_id":"user-1"}`))
	assertNoFrame(t, client)

	// A different resource the client never subscribed to must not match.
	hub.routeConnector("tenant-A", []byte(
		`{"kind":"status","tenant_id":"tenant-A","gateway_id":"gw-1"}`))
	assertNoFrame(t, client)
}

func TestRouteMemberDropsMalformedFrame(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceMember, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeMember(tenant, []byte(`{not json`))
	assertNoFrame(t, client)

	// An empty tenant (bad subject token) drops the frame too.
	hub.routeMember("", []byte(
		`{"kind":"removed","tenant_id":"tenant-A","user_id":"user-1"}`))
	assertNoFrame(t, client)
}
