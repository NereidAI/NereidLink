// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// acl_test.go pins the device topic authorization rules (ADR-0008). These
// cases are written deny-first because the failure mode that matters is not
// "a legitimate device was briefly refused" but "a device reached a topic it
// did not own", and a permissive regression here is silent in production.
package mqttapi

import "testing"

// TestAuthorizePublishDeviceTopics verifies the always-granted device uplink
// set is reachable without any capability.
func TestAuthorizePublishDeviceTopics(t *testing.T) {
	for _, topic := range []string{
		TopicDeviceTelemetry,
		TopicDeviceAttributes,
		TopicDeviceAttributesGet,
		TopicDeviceEvents,
		TopicDeviceShadowReport,
	} {
		if !Authorize(ActionPublish, topic, nil) {
			t.Errorf("device topic %q denied without capabilities", topic)
		}
	}
}

// TestAuthorizeGatewayCapability verifies the gateway topic set is gated on
// the gateway capability and invisible without it. This is the case that
// keeps one tenant's gateway from writing another tenant's sub-devices.
func TestAuthorizeGatewayCapability(t *testing.T) {
	gatewayOnly := []string{
		TopicGatewayTelemetry,
		TopicGatewayAttributes,
		TopicGatewayAttributesGet,
		TopicGatewayConnect,
		TopicGatewayDisconnect,
		TopicGatewayHello,
		TopicGatewayManifest,
		TopicGatewayProfiles,
		TopicGatewayDiscovery,
		TopicGatewayProbeResponse,
		TopicGatewayCommandResponse,
		TopicGatewayShadowReport,
		TopicGatewayOTAProgress,
	}
	for _, topic := range gatewayOnly {
		if Authorize(ActionPublish, topic, nil) {
			t.Errorf("gateway topic %q allowed without gateway capability", topic)
		}
		if !Authorize(ActionPublish, topic, []string{CapabilityGateway}) {
			t.Errorf("gateway topic %q denied with gateway capability", topic)
		}
	}
	// OTA task is a gateway downlink: the gateway subscribes, it never
	// publishes it.
	if Authorize(ActionPublish, TopicGatewayOTATask, []string{CapabilityGateway}) {
		t.Error("ota/task must not be a gateway publish topic")
	}
	if !Authorize(ActionSubscribe, TopicGatewayOTATask, []string{CapabilityGateway}) {
		t.Error("ota/task must be a gateway subscription")
	}
	if Authorize(ActionSubscribe, TopicGatewayOTATask, nil) {
		t.Error("ota/task subscription requires gateway capability")
	}
}

// TestAuthorizeCommandResponsePrefix verifies the command response uplink is
// matched by prefix, since the command ID is appended to the topic.
func TestAuthorizeCommandResponsePrefix(t *testing.T) {
	if !Authorize(ActionPublish, PrefixDeviceCommandResponse+"abc-123", nil) {
		t.Error("command response topic denied; the command ID suffix must be accepted")
	}
	// The bare prefix with no command ID is still a valid publish.
	if !Authorize(ActionPublish, PrefixDeviceCommandResponse, nil) {
		t.Error("bare command response prefix denied")
	}
}

// TestAuthorizeSubscribeAllowlist verifies SUBSCRIBE matches only the declared
// downlink filters exactly, including the trailing wildcard token.
func TestAuthorizeSubscribeAllowlist(t *testing.T) {
	for _, filter := range []string{
		FilterDeviceCommands,
		TopicDeviceAttributesDown,
		TopicDeviceAttributesGetAck,
		TopicDeviceShadowDesired,
	} {
		if !Authorize(ActionSubscribe, filter, nil) {
			t.Errorf("downlink filter %q denied", filter)
		}
	}
	for _, filter := range []string{
		TopicDeviceTelemetry,
		TopicDeviceAttributes,
		TopicDeviceAttributesGet,
		FilterDeviceCommands + "extra",
		TopicGatewayTelemetry,
		"",
	} {
		if Authorize(ActionSubscribe, filter, nil) {
			t.Errorf("subscribe filter %q allowed", filter)
		}
	}
}

// TestAuthorizeProbeTopics verifies the on-demand probe pair: the response
// uplink requires the gateway capability, the probe downlink is gateway-only
// subscribable and not publishable, and the uplink topic is not subscribable.
func TestAuthorizeProbeTopics(t *testing.T) {
	if Authorize(ActionPublish, TopicGatewayProbeResponse, nil) {
		t.Error("probe response allowed without gateway capability")
	}
	if !Authorize(ActionPublish, TopicGatewayProbeResponse, []string{CapabilityGateway}) {
		t.Error("probe response denied with gateway capability")
	}
	if Authorize(ActionSubscribe, TopicGatewayProbeResponse, []string{CapabilityGateway}) {
		t.Error("probe response uplink must not be subscribable")
	}
	if Authorize(ActionSubscribe, TopicGatewayProbe, nil) {
		t.Error("probe downlink subscribable without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayProbe, []string{CapabilityGateway}) {
		t.Error("probe downlink denied with gateway capability")
	}
	if Authorize(ActionPublish, TopicGatewayProbe, []string{CapabilityGateway}) {
		t.Error("a device must not publish on the probe downlink topic")
	}
}

// TestAuthorizeDiscoveryTopics verifies the discovery uplink/result pair:
// publishing requires the gateway capability, the uplink is not subscribable,
// and the result downlink requires the gateway capability while remaining
// unpublishable by a device.
func TestAuthorizeDiscoveryTopics(t *testing.T) {
	if Authorize(ActionPublish, TopicGatewayDiscovery, nil) {
		t.Error("discovery uplink allowed without gateway capability")
	}
	if !Authorize(ActionPublish, TopicGatewayDiscovery, []string{CapabilityGateway}) {
		t.Error("discovery uplink denied with gateway capability")
	}
	if Authorize(ActionSubscribe, TopicGatewayDiscovery, []string{CapabilityGateway}) {
		t.Error("discovery uplink topic must not be subscribable")
	}
	if Authorize(ActionSubscribe, TopicGatewayDiscoveryResult, nil) {
		t.Error("discovery result subscribable without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayDiscoveryResult, []string{CapabilityGateway}) {
		t.Error("discovery result denied with gateway capability")
	}
	if Authorize(ActionPublish, TopicGatewayDiscoveryResult, []string{CapabilityGateway}) {
		t.Error("a device must not publish on the result downlink topic")
	}
}

// TestAuthorizeGatewayCommandTopics verifies the gateway-relayed command
// pair: the response uplink requires the gateway capability and must not be
// subscribable, while the command downlink is gateway-only subscribable and
// never publishable by a device.
func TestAuthorizeGatewayCommandTopics(t *testing.T) {
	if Authorize(ActionPublish, TopicGatewayCommandResponse, nil) {
		t.Error("gateway command response allowed without gateway capability")
	}
	if !Authorize(ActionPublish, TopicGatewayCommandResponse, []string{CapabilityGateway}) {
		t.Error("gateway command response denied with gateway capability")
	}
	if Authorize(ActionSubscribe, TopicGatewayCommandResponse, []string{CapabilityGateway}) {
		t.Error("command response uplink must not be subscribable")
	}
	if Authorize(ActionSubscribe, TopicGatewayCommand, nil) {
		t.Error("gateway command subscribable without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayCommand, []string{CapabilityGateway}) {
		t.Error("gateway command denied with gateway capability")
	}
	if Authorize(ActionPublish, TopicGatewayCommand, []string{CapabilityGateway}) {
		t.Error("a device must not publish on the gateway command downlink topic")
	}
}

// TestAuthorizeGatewayAttributesPush verifies the gateway-side shared/server
// attribute push is a gateway-only downlink subscription.
func TestAuthorizeGatewayAttributesPush(t *testing.T) {
	if Authorize(ActionSubscribe, TopicGatewayAttributesPush, nil) {
		t.Error("gateway attribute push subscribable without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayAttributesPush, []string{CapabilityGateway}) {
		t.Error("gateway attribute push denied with gateway capability")
	}
	if Authorize(ActionPublish, TopicGatewayAttributesPush, []string{CapabilityGateway}) {
		t.Error("a device must not publish on the gateway attribute push topic")
	}
}

// TestAuthorizeGatewayShadow verifies the gateway-proxied shadow pair: the
// batched report uplink requires the gateway capability and is not
// subscribable, while the desired downlink is gateway-only subscribable and
// never publishable by a device.
func TestAuthorizeGatewayShadow(t *testing.T) {
	if Authorize(ActionPublish, TopicGatewayShadowReport, nil) {
		t.Error("gateway shadow report allowed without gateway capability")
	}
	if !Authorize(ActionPublish, TopicGatewayShadowReport, []string{CapabilityGateway}) {
		t.Error("gateway shadow report denied with gateway capability")
	}
	if Authorize(ActionSubscribe, TopicGatewayShadowReport, []string{CapabilityGateway}) {
		t.Error("shadow report uplink must not be subscribable")
	}
	if Authorize(ActionSubscribe, TopicGatewayShadowDesired, nil) {
		t.Error("gateway shadow desired subscribable without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayShadowDesired, []string{CapabilityGateway}) {
		t.Error("gateway shadow desired denied with gateway capability")
	}
	if Authorize(ActionPublish, TopicGatewayShadowDesired, []string{CapabilityGateway}) {
		t.Error("a device must not publish on the gateway shadow desired topic")
	}
}

// TestAuthorizeRejectsUnknownAction verifies an unrecognized action is denied.
// A caller that misspells the action must not silently receive publish rights
// by falling through to a permissive default.
func TestAuthorizeRejectsUnknownAction(t *testing.T) {
	for _, action := range []string{"", "PUBLISH", "Publish", "write", "admin", "*"} {
		if Authorize(action, TopicDeviceTelemetry, []string{CapabilityGateway}) {
			t.Errorf("unknown action %q allowed on a device topic", action)
		}
	}
}

// TestAuthorizeDenyByDefault verifies unlisted topics are refused for both
// directions, including near-misses of the real templates.
func TestAuthorizeDenyByDefault(t *testing.T) {
	caps := []string{CapabilityGateway}
	for _, topic := range []string{
		"v1/me/other",
		"v1/me/",
		"v1/admin/config",
		"$SYS/broker/version",
		TopicDeviceTelemetry + "/extra",
		"v1/gateway/",
	} {
		if Authorize(ActionPublish, topic, caps) {
			t.Errorf("unlisted publish topic %q allowed", topic)
		}
	}
}

// TestAuthorizeCapabilityIsNotAPrefixMatch verifies capability matching is
// exact, so a capability named "gateway-admin" does not unlock the gateway set.
func TestAuthorizeCapabilityIsNotAPrefixMatch(t *testing.T) {
	for _, capability := range []string{"Gateway", "GATEWAY", "gateway-admin", "edge-control"} {
		if Authorize(ActionPublish, TopicGatewayTelemetry, []string{capability}) {
			t.Errorf("capability %q unlocked the gateway topic set", capability)
		}
	}
}

// TestAuthorizePublishDoesNotAcceptDownlinkFilters verifies the publish
// direction cannot be satisfied with a subscribe filter, which would otherwise
// let a device read the shadow desired state by publishing to it.
func TestAuthorizePublishDoesNotAcceptDownlinkFilters(t *testing.T) {
	for _, topic := range []string{
		TopicDeviceShadowDesired,
		TopicDeviceAttributesDown,
	} {
		if Authorize(ActionPublish, topic, []string{CapabilityGateway}) {
			t.Errorf("downlink filter %q accepted in the publish direction", topic)
		}
	}
}

// TestAllowedWriteTopicsComposition documents that the gateway capability
// widens the device set rather than replacing it.
func TestAllowedWriteTopicsComposition(t *testing.T) {
	plain := AllowedWriteTopics(nil)
	gateway := AllowedWriteTopics([]string{CapabilityGateway})
	if len(gateway) <= len(plain) {
		t.Fatalf("gateway set (%d) should extend the device set (%d)", len(gateway), len(plain))
	}
	for _, topic := range plain {
		found := false
		for _, granted := range gateway {
			if granted == topic {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("device topic %q lost when the gateway capability is granted", topic)
		}
	}
}

// TestAuthorizeAttributeFetch verifies the on-demand attribute pull pair:
// get uplink and get/ack subscription for direct devices, and the
// gateway-proxied variants gated on the gateway capability.
func TestAuthorizeAttributeFetch(t *testing.T) {
	if !Authorize(ActionPublish, TopicDeviceAttributesGet, nil) {
		t.Error("device attribute get must be publishable without capabilities")
	}
	if !Authorize(ActionSubscribe, TopicDeviceAttributesGetAck, nil) {
		t.Error("device attribute get/ack must be subscribable without capabilities")
	}
	if Authorize(ActionPublish, TopicDeviceAttributesGetAck, nil) {
		t.Error("device must not publish on its own attribute get/ack topic")
	}
	if Authorize(ActionSubscribe, TopicDeviceAttributesGet, nil) {
		t.Error("device attribute get uplink must not be subscribable")
	}
	if Authorize(ActionPublish, TopicGatewayAttributesGet, nil) {
		t.Error("gateway attribute get allowed without gateway capability")
	}
	if !Authorize(ActionPublish, TopicGatewayAttributesGet, []string{CapabilityGateway}) {
		t.Error("gateway attribute get denied with gateway capability")
	}
	if Authorize(ActionSubscribe, TopicGatewayAttributesGetAck, nil) {
		t.Error("gateway attribute get/ack subscribed without gateway capability")
	}
	if !Authorize(ActionSubscribe, TopicGatewayAttributesGetAck, []string{CapabilityGateway}) {
		t.Error("gateway attribute get/ack denied with gateway capability")
	}
}
