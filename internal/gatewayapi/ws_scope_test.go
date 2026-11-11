// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ws_scope_test.go proves the WebSocket push plane isolates tenants and
// honors data scope without needing a live socket: the routing decision lives
// in Client predicates and subjectToken, both of which are pure. The hub
// fanout only forwards a frame when the matching predicate is true, so these
// assertions are exactly the cross-tenant leak boundary.

package gatewayapi

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/nereidai/nereidlink/internal/rbac"
)

func TestSubjectTokenTenantSegments(t *testing.T) {
	cases := []struct {
		subject string
		index   int
		want    string
	}{
		{"nl.telemetry.s2.aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.7", 4, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{"nl.presence.tenant-abc.dev-1", 3, "tenant-abc"},
		{"nl.alarm.tenant-abc.dev-1", 3, "tenant-abc"},
		{"nl.notification.tenant-abc", 3, "tenant-abc"},
		{"nl.command.task.tenant-abc", 4, "tenant-abc"},
		{"nl.probe.task.tenant-abc", 4, "tenant-abc"},
		{"nl.ota.task.tenant-abc", 4, "tenant-abc"},
		{"nl.connector.event.tenant-abc", 4, "tenant-abc"},
		{"nl.attribute.event.tenant-abc.dev-1", 4, "tenant-abc"},
		{"nl.shadow.event.tenant-abc.dev-1", 4, "tenant-abc"},
		{"nl.member.event.tenant-abc", 4, "tenant-abc"},
	}
	for _, tc := range cases {
		if got := subjectToken(tc.subject, tc.index); got != tc.want {
			t.Errorf("subjectToken(%q,%d) = %q, want %q", tc.subject, tc.index, got, tc.want)
		}
	}
	// Out-of-range and malformed subjects yield "" so a bad frame is dropped
	// rather than delivered against an empty/forged tenant.
	for _, in := range []string{"nl", "nl.telemetry", ""} {
		if got := subjectToken(in, 4); got != "" {
			t.Errorf("subjectToken(%q,4) = %q, want empty", in, got)
		}
	}
}

func TestClientDeviceSubscriptionTenantIsolation(t *testing.T) {
	const dev = "dev-1"
	c := NewClient(&websocket.Conn{}, nil, 0)

	// An explicit device subscription scoped to tenant A only.
	c.subscribe("sub1", ResourceTelemetry, "tenant-A", []string{dev}, false)

	if !c.wants(ResourceTelemetry, "tenant-A", dev) {
		t.Fatal("own tenant/device must match")
	}
	if c.wants(ResourceTelemetry, "tenant-B", dev) {
		t.Fatal("LEAK: same device id matched a different tenant")
	}
	if c.wants(ResourceTelemetry, "tenant-A", "dev-other") {
		t.Fatal("a device not in the subscription must not match")
	}
	if c.wants(ResourcePresence, "tenant-A", dev) {
		t.Fatal("a different resource type must not match")
	}
	if c.wantsWildcard(ResourceTelemetry, "tenant-A") {
		t.Fatal("an explicit-device subscription is not a wildcard")
	}
}

func TestClientWildcardScopedToTenant(t *testing.T) {
	c := NewClient(&websocket.Conn{}, nil, 0)

	// A wildcard is only recorded for an unrestricted caller, and it must be
	// bound to the exact tenant it was opened against.
	c.subscribe("sub1", ResourceAlarm, "tenant-A", nil, true)

	if !c.wantsWildcard(ResourceAlarm, "tenant-A") {
		t.Fatal("own tenant wildcard must match")
	}
	if c.wantsWildcard(ResourceAlarm, "tenant-B") {
		t.Fatal("LEAK: wildcard matched another tenant")
	}
	if c.wantsWildcard(ResourceTelemetry, "tenant-A") {
		t.Fatal("wildcard must not leak across resource types")
	}
}

func TestClientTenantWideResourceIsolation(t *testing.T) {
	c := NewClient(&websocket.Conn{}, nil, 0)

	// Notifications and task-progress streams carry no device filter but are
	// tenant-scoped.
	c.subscribe("sub1", ResourceNotification, "tenant-A", nil, true)

	if !c.wantsTenant(ResourceNotification, "tenant-A") {
		t.Fatal("own tenant notification stream must match")
	}
	if c.wantsTenant(ResourceNotification, "tenant-B") {
		t.Fatal("LEAK: tenant-wide stream delivered to another tenant")
	}
	if c.wantsTenant(ResourceCommandTask, "tenant-A") {
		t.Fatal("an unsubscribed tenant-wide resource must not match")
	}
}

func TestClientScopedWildcardMaterializedAsDeviceSet(t *testing.T) {
	const dev = "visible-dev"
	c := NewClient(&websocket.Conn{}, nil, 0)

	// A customer-scoped member's wildcard is materialized server-side to its
	// visible device set: wildcard=false, with the visible ids listed.
	c.subscribe("sub1", ResourcePresence, "tenant-A", []string{dev}, false)

	if c.wantsWildcard(ResourcePresence, "tenant-A") {
		t.Fatal("materialized scope must not be a server-side wildcard")
	}
	if !c.wants(ResourcePresence, "tenant-A", dev) {
		t.Fatal("the one visible device must match")
	}
	if c.wants(ResourcePresence, "tenant-A", "hidden-dev") {
		t.Fatal("LEAK: a device outside the materialized set matched")
	}
}

func TestResourceAndPointMapping(t *testing.T) {
	if !isDeviceScopedResource(ResourceTelemetry) ||
		!isDeviceScopedResource(ResourcePresence) ||
		!isDeviceScopedResource(ResourceAlarm) ||
		!isDeviceScopedResource(ResourceDeviceEvent) ||
		!isDeviceScopedResource(ResourceAttribute) ||
		!isDeviceScopedResource(ResourceShadow) {
		t.Fatal("telemetry/presence/alarm/device_event/attribute/shadow must be device-scoped")
	}
	if isDeviceScopedResource(ResourceNotification) ||
		isDeviceScopedResource(ResourceCommandTask) ||
		isDeviceScopedResource(ResourceOTATask) ||
		isDeviceScopedResource(ResourceProbeTask) ||
		isDeviceScopedResource(ResourceConnector) ||
		isDeviceScopedResource(ResourceModelInventory) ||
		isDeviceScopedResource(ResourceSecretBinding) ||
		isDeviceScopedResource(ResourceMember) {
		t.Fatal("notifications, task progress, connector/model/secret/member events are tenant-wide, not device-scoped")
	}
	if subscriptionPoint(ResourceTelemetry) == "" ||
		subscriptionPoint("bogus") != "" {
		t.Fatal("subscriptionPoint must map known resources and reject unknown")
	}
	if subscriptionPoint(ResourceAttribute) != rbac.PointDeviceRead {
		t.Fatalf("attribute stream must require %s, got %q",
			rbac.PointDeviceRead, subscriptionPoint(ResourceAttribute))
	}
	if subscriptionPoint(ResourceShadow) != rbac.PointDeviceRead {
		t.Fatalf("shadow stream must require %s, got %q",
			rbac.PointDeviceRead, subscriptionPoint(ResourceShadow))
	}
	if subscriptionPoint(ResourceConnector) != rbac.PointConnectorRead {
		t.Fatalf("connector stream must require %s, got %q",
			rbac.PointConnectorRead, subscriptionPoint(ResourceConnector))
	}
	if subscriptionPoint(ResourceModelInventory) != rbac.PointConnectorRead {
		t.Fatalf("model inventory stream must require %s, got %q",
			rbac.PointConnectorRead, subscriptionPoint(ResourceModelInventory))
	}
}
