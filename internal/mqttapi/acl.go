// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// acl.go is the single source of truth for device topic authorization
// (ADR-0008 "ACL hook: authorize by tenant, device, and topic template").
//
// It lives here, beside the topic constants it interprets, for one reason: two
// enforcement points must not be able to disagree. The MQTT broker hook and
// the registry gRPC Authorize RPC both decide whether a device may touch a
// topic, and a platform that trusts an external ingress connector to ask the
// registry has to get the same answer the in-process broker would give. An
// ACL duplicated per ingress is an ACL that will drift, and an ACL that
// drifts is a privilege escalation that no test catches because each copy
// passes its own tests.
//
// The rules are deliberately a small exact-match allowlist over the frozen
// templates rather than a pattern language. "me" is already resolved to the
// authenticated identity by the broker before the ACL runs, so the concrete
// topic is directly comparable. Deny is the default for anything not named.

package mqttapi

import "strings"

// Capability identifiers matched against registry responses.
const (
	// CapabilityGateway allows the gateway topic set. A device that lacks it
	// cannot publish sub-device telemetry or gateway control topics, which is
	// what keeps one tenant's gateway from writing another tenant's devices.
	CapabilityGateway = "gateway"
)

// ACL actions accepted by Authorize. Anything else is denied: an unrecognized
// action must never be treated as permissive, because a future caller that
// spells the action differently would otherwise be granted everything.
const (
	// ActionPublish is an uplink PUBLISH from the device.
	ActionPublish = "publish"
	// ActionSubscribe is a SUBSCRIBE from the device.
	ActionSubscribe = "subscribe"
)

// AllowedWriteTopics returns the publish topics granted for capabilities.
// The device set is always available; the gateway set requires the gateway
// capability declared by the registry.
func AllowedWriteTopics(capabilities []string) []string {
	topics := []string{
		TopicDeviceTelemetry,
		TopicDeviceAttributes,
		TopicDeviceAttributesGet,
		TopicDeviceEvents,
		TopicDeviceShadowReport,
	}
	for _, capability := range capabilities {
		if capability == CapabilityGateway {
			topics = append(topics,
				TopicGatewayTelemetry,
				TopicGatewayAttributes,
				TopicGatewayAttributesGet,
				TopicGatewayConnect,
				TopicGatewayDisconnect,
				TopicGatewayHello,
				TopicGatewayHeartbeat,
				TopicGatewayGoodbye,
				TopicGatewayLinkHealth,
				TopicGatewayManifest,
				TopicGatewayProfiles,
				TopicGatewayDiscovery,
				TopicGatewayProbeResponse,
				TopicGatewayConnectorStatus,
				TopicGatewaySecretsStatus,
				TopicGatewayProfilesStatus,
				TopicGatewayCommandResponse,
				TopicGatewayOperationsResult,
				TopicGatewayShadowReport,
				TopicGatewayOTAProgress)
		}
	}
	return topics
}

// SubscriptionAllowed reports whether a SUBSCRIBE filter is inside the set a
// device is allowed to receive. Device downlink filters are available to
// every device; the gateway negotiation ack additionally requires the
// gateway capability because only a gateway performs hello. Filters must
// match one of the declared downlink filters exactly, including the
// trailing wildcard token.
func SubscriptionAllowed(topic string, capabilities []string) bool {
	if topic == FilterDeviceCommands ||
		topic == TopicDeviceAttributesDown ||
		topic == TopicDeviceAttributesGetAck ||
		topic == TopicDeviceShadowDesired {
		return true
	}
	if topic == TopicGatewayHelloAck || topic == TopicGatewayLinkHealthAck ||
		topic == FilterGatewayConnectors ||
		topic == FilterGatewaySecrets || topic == FilterGatewayOperations ||
		topic == FilterGatewayProfiles ||
		topic == TopicGatewayDiscoveryResult || topic == TopicGatewayProbe ||
		topic == TopicGatewayOTATask || topic == TopicGatewayCommand ||
		topic == TopicGatewayAttributesPush ||
		topic == TopicGatewayAttributesGetAck ||
		topic == TopicGatewayShadowDesired {
		for _, capability := range capabilities {
			if capability == CapabilityGateway {
				return true
			}
		}
	}
	return false
}

// Authorize reports whether a device holding capabilities may perform action
// on topic.
//
// This is the one decision function every ingress path must call. It is
// deny-by-default in three distinct ways: an unknown action is denied, an
// unlisted publish topic is denied, and an unlisted subscription filter is
// denied. There is no wildcard in the publish direction, so a device cannot
// broaden its own access by subscribing broadly.
func Authorize(action, topic string, capabilities []string) bool {
	switch action {
	case ActionSubscribe:
		return SubscriptionAllowed(topic, capabilities)
	case ActionPublish:
		// Command responses are matched by prefix because the command ID is
		// appended to the topic. The prefix sits outside the command
		// subscription filter so a device never receives its own response.
		if strings.HasPrefix(topic, PrefixDeviceCommandResponse) {
			return true
		}
		for _, allowed := range AllowedWriteTopics(capabilities) {
			if topic == allowed {
				return true
			}
		}
		return false
	default:
		return false
	}
}
