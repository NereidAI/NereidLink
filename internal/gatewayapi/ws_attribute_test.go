// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ws_attribute_test.go proves the attribute real-time resource is routed per
// device and isolated by tenant/resource exactly like the other device-scoped
// streams, without a live socket or NATS.
package gatewayapi

import (
	"encoding/json"
	"testing"
	"time"
)

// newTestClient builds a Client without its writer goroutine (no live socket)
// so routed frames can be read deterministically from sendCh.
func newTestClient() *Client {
	return &Client{
		subscriptions:   map[string]*subscription{},
		devices:         map[subscriptionKey]map[string]struct{}{},
		wildcardTenants: map[subscriptionKey]struct{}{},
		sendCh:          make(chan []byte, wsSendBuffer),
		done:            make(chan struct{}),
	}
}

// drainFrame reads one serialized data frame a router enqueued, failing the
// test when nothing arrives.
func drainFrame(t *testing.T, client *Client) dataFrame {
	t.Helper()
	select {
	case raw := <-client.sendCh:
		var frame dataFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("frame decode: %v", err)
		}
		return frame
	case <-time.After(time.Second):
		t.Fatal("no frame routed")
		return dataFrame{}
	}
}

// assertNoFrame fails if the router enqueues a frame within the window.
func assertNoFrame(t *testing.T, client *Client) {
	t.Helper()
	select {
	case raw := <-client.sendCh:
		t.Fatalf("unexpected frame delivered: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRouteAttributeDeliversToSubscribedDevice(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant, dev = "tenant-A", "dev-1"
	client := newTestClient()
	client.subscribe("sub1", ResourceAttribute, tenant, []string{dev}, false)
	hub.clients[client] = struct{}{}

	hub.routeAttribute(tenant, []byte(
		`{"tenant_id":"tenant-A","device_id":"dev-1","scope":"CLIENT",`+
			`"key":"temp","value":42,"updated_at":"2026-10-06T00:00:00Z",`+
			`"source":"device"}`))

	frame := drainFrame(t, client)
	if frame.Type != ResourceAttribute {
		t.Fatalf("frame type=%q want attribute", frame.Type)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok || data["key"] != "temp" {
		t.Fatalf("unexpected payload: %#v", frame.Data)
	}

	// A frame for another device of the same tenant must not be delivered.
	hub.routeAttribute(tenant, []byte(
		`{"tenant_id":"tenant-A","device_id":"dev-other","scope":"CLIENT","key":"x","value":1}`))
	assertNoFrame(t, client)

	// A frame for another tenant must not be delivered either.
	hub.routeAttribute("tenant-B", []byte(
		`{"tenant_id":"tenant-B","device_id":"dev-1","scope":"CLIENT","key":"x","value":1}`))
	assertNoFrame(t, client)
}

func TestRouteAttributeWildcardDataScope(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceAttribute, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeAttribute(tenant, []byte(
		`{"tenant_id":"tenant-A","device_id":"dev-9","scope":"SERVER_SCOPE","key":"k","value":"v"}`))
	frame := drainFrame(t, client)
	if frame.Type != ResourceAttribute {
		t.Fatalf("frame type=%q want attribute", frame.Type)
	}
}

func TestRouteAttributeDropsMalformedFrame(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant = "tenant-A"
	client := newTestClient()
	client.subscribe("sub1", ResourceAttribute, tenant, nil, true)
	hub.clients[client] = struct{}{}

	hub.routeAttribute(tenant, []byte(`{not json`))
	assertNoFrame(t, client)
}
