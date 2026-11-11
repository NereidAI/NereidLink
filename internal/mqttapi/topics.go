// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package mqttapi is the single source of truth for the MQTT topic
// contract shared by direct devices, gateways (including NereidEdge) and the
// broker-side hooks.
//
// These topics are a frozen wire contract rather than tunable parameters, so
// they live as compiled constants here instead of the runtime configuration
// file. Operational values (listen addresses, thresholds, quotas) are
// configuration; wire identifiers are constants.
package mqttapi

// Topic templates for directly connected devices.
const (
	// TopicDeviceTelemetry accepts JSON telemetry frames from a device.
	TopicDeviceTelemetry = "v1/me/telemetry"

	// TopicDeviceAttributes accepts client-scope attribute reports.
	TopicDeviceAttributes = "v1/me/attributes"

	// TopicDeviceAttributesGet accepts a device request to fetch the
	// platform-managed attributes (SERVER_SCOPE/SHARED_SCOPE) so a device
	// that was offline during the down push can recover them on demand.
	TopicDeviceAttributesGet = "v1/me/attributes/get"

	// TopicDeviceAttributesGetAck carries the requested scopes back to the
	// requesting device (unless it named its own response_topic).
	TopicDeviceAttributesGetAck = "v1/me/attributes/get/ack"

	// TopicDeviceEvents accepts lifecycle and domain events.
	TopicDeviceEvents = "v1/me/events"

	// TopicDeviceShadowReport accepts device shadow reports.
	TopicDeviceShadowReport = "v1/me/shadow/report"

	// TopicDeviceShadowDesired carries desired-state pushes.
	TopicDeviceShadowDesired = "v1/me/shadow/desired"

	// PrefixDeviceCommands is the downlink topic prefix under which a
	// command is delivered, suffixed by the command name.
	PrefixDeviceCommands = "v1/me/cmd/"

	// FilterDeviceCommands is the command filter a device subscribes to.
	FilterDeviceCommands = "v1/me/cmd/#"

	// PrefixDeviceCommandResponse is the uplink prefix under which a device
	// publishes a response, suffixed by the command ID. It is deliberately
	// outside the command subscription filter so a device never receives
	// its own response.
	PrefixDeviceCommandResponse = "v1/me/cmdres/"

	// TopicDeviceAttributesDown carries server/shared attribute pushes.
	TopicDeviceAttributesDown = "v1/me/attributes/down"
)

// Topic templates for gateways. NereidEdge is one gateway type and uses the
// same templates; no separate topic space exists.
const (
	// TopicGatewayTelemetry accepts telemetry for attached sub-devices.
	TopicGatewayTelemetry = "v1/gateway/telemetry"

	// TopicGatewayAttributes accepts sub-device attribute reports.
	TopicGatewayAttributes = "v1/gateway/attributes"

	// TopicGatewayAttributesGet accepts a gateway request to fetch the
	// platform-managed attributes of one attached sub-device.
	TopicGatewayAttributesGet = "v1/gateway/attributes/get"

	// TopicGatewayAttributesGetAck carries the requested scopes back to the
	// requesting gateway, naming the sub-device (unless response_topic used).
	TopicGatewayAttributesGetAck = "v1/gateway/attributes/get/ack"

	// TopicGatewayConnect announces a sub-device session start.
	TopicGatewayConnect = "v1/gateway/connect"

	// TopicGatewayDisconnect announces a sub-device session end.
	TopicGatewayDisconnect = "v1/gateway/disconnect"

	// TopicGatewayHello is the permanent negotiation bootstrap topic.
	TopicGatewayHello = "v1/gateway/hello"

	// TopicGatewayHelloAck is the default downlink topic carrying the
	// negotiation response when the edge does not name a ResponseTopic.
	TopicGatewayHelloAck = "v1/gateway/hello/ack"

	// TopicGatewayManifest carries the periodic sub-device manifest.
	TopicGatewayManifest = "v1/gateway/manifest"

	// TopicGatewayProfiles accepts edge-reported EdgeX device profiles
	// (name + deviceResources) that NL caches and can adopt into native
	// device profiles. Fire-and-forget, QoS1; the edge replays the full set
	// after each hello and the platform de-duplicates by content hash.
	TopicGatewayProfiles = "v1/gateway/profiles"

	// TopicGatewayConnectorSpec carries a desired connector spec push
	// (create or update). The edge subscribes; delivery is idempotent and
	// re-sent on every (re)hello (ADR-0026).
	TopicGatewayConnectorSpec = "v1/gateway/connectors/spec"

	// TopicGatewayConnectorDelete carries a desired connector removal.
	TopicGatewayConnectorDelete = "v1/gateway/connectors/delete"

	// TopicGatewayConnectorStatus accepts the edge-reported connector
	// lifecycle state (CREATING/APPLIED/FAILED/DEGRADED plus revision).
	TopicGatewayConnectorStatus = "v1/gateway/connectors/status"

	// TopicGatewaySecretsSync carries the full desired secret delivery set
	// for one gateway (ADR-0034). The edge subscribes; idempotent and
	// re-sent on every (re)hello.
	TopicGatewaySecretsSync = "v1/gateway/secrets/sync"

	// TopicGatewaySecretsStatus accepts edge-reported secret binding state
	// (APPLIED/FAILED/REMOVED).
	TopicGatewaySecretsStatus = "v1/gateway/secrets/status"

	// TopicGatewayProfilesSpec carries the full desired device-profile set
	// for one gateway (ADR-0038). The edge subscribes; delivery is
	// idempotent and re-sent on every (re)hello. Each entry names a library
	// profile, its revision, the render format, and the document.
	TopicGatewayProfilesSpec = "v1/gateway/profiles/spec"

	// TopicGatewayProfilesStatus accepts edge-reported profile delivery
	// state (APPLIED/FAILED/REJECTED/REMOVED) keyed by profile name.
	TopicGatewayProfilesStatus = "v1/gateway/profiles/status"

	// FilterGatewayProfiles is the single downlink filter for profile
	// pushes.
	FilterGatewayProfiles = "v1/gateway/profiles/#"

	// FilterGatewaySecrets is the single downlink filter for secret pushes.
	FilterGatewaySecrets = "v1/gateway/secrets/#"

	// FilterGatewayConnectors is the single downlink filter the edge
	// subscribes to for spec and delete pushes.
	FilterGatewayConnectors = "v1/gateway/connectors/#"

	// TopicGatewayHeartbeat carries the application-level liveness ping.
	// It is distinct from the MQTT transport keepalive: a live TCP link
	// with a silent edge application must still be detectable (ADR-0026).
	TopicGatewayHeartbeat = "v1/gateway/heartbeat"

	// TopicGatewayGoodbye carries a graceful shutdown notice. It is also
	// the pre-registered MQTT Last Will and Testament topic, so an
	// ungraceful transport death is delivered here by the broker with a
	// non-planned reason.
	TopicGatewayGoodbye = "v1/gateway/goodbye"

	// TopicGatewayLinkHealth accepts edge-local link watchdog episode
	// reports (unplanned down/up intervals, migration 0075). QoS1, after
	// hello; the server answers on TopicGatewayLinkHealthAck.
	TopicGatewayLinkHealth = "v1/gateway/linkhealth"

	// TopicGatewayLinkHealthAck carries the accept/duplicate/reject
	// response unless the report names its own response_topic.
	TopicGatewayLinkHealthAck = "v1/gateway/linkhealth/ack"

	// TopicGatewayOTATask carries firmware task dispatch.
	TopicGatewayOTATask = "v1/gateway/ota/task"

	// TopicGatewayOTAProgress carries firmware task progress reports.
	TopicGatewayOTAProgress = "v1/gateway/ota/progress"

	// TopicGatewayDiscovery accepts a gateway-reported list of local devices
	// that are not yet registered. When auto-provisioning is enabled the
	// platform creates them bound to this gateway and replies on
	// TopicGatewayDiscoveryResult with each local_id mapped to a device UUID.
	TopicGatewayDiscovery = "v1/gateway/discovery"

	// TopicGatewayDiscoveryResult is the default downlink topic carrying the
	// local_id -> device_id mapping. The edge subscribes to it.
	TopicGatewayDiscoveryResult = "v1/gateway/discovery/result"

	// TopicGatewayProbe carries the aggregated on-demand probe request from
	// NL to one gateway (ADR-0026 section 9). The edge subscribes to it and
	// answers per device on TopicGatewayProbeResponse.
	TopicGatewayProbe = "v1/gateway/probe"

	// TopicGatewayProbeResponse accepts the per-device probe result the
	// gateway returns, correlated by probe_id and device.
	TopicGatewayProbeResponse = "v1/gateway/probe/response"

	// TopicGatewayCommand carries a command addressed to one sub-device
	// through its owning gateway (NL -> Edge). The edge subscribes to it;
	// the frame names the target sub-device so one subscription serves
	// every attached device.
	TopicGatewayCommand = "v1/gateway/command"

	// TopicGatewayCommandResponse accepts the sub-device command result the
	// gateway returns (Edge -> NL), correlated by command_id and device.
	TopicGatewayCommandResponse = "v1/gateway/command/response"

	// TopicGatewayAttributesPush carries server/shared attribute pushes to
	// one sub-device through its owning gateway (NL -> Edge). It is the
	// gateway counterpart of TopicDeviceAttributesDown.
	TopicGatewayAttributesPush = "v1/gateway/attributes/push"

	// TopicGatewayShadowReport accepts batched reported-state patches for
	// the attached sub-devices a gateway proxies (Edge -> NL). Each entry
	// names its own sub-device and the desired version it applied.
	TopicGatewayShadowReport = "v1/gateway/shadow/report"

	// TopicGatewayShadowDesired carries full desired-state pushes to
	// sub-devices through their owning gateway (NL -> Edge). One
	// subscription serves every attached device; each frame entry names
	// the target sub-device. It is the gateway counterpart of
	// TopicDeviceShadowDesired.
	TopicGatewayShadowDesired = "v1/gateway/shadow/desired"

	// TopicGatewayOperationsInvoke carries an immediate whole-gateway
	// operation request (NL -> Edge, ADR-0035). The edge subscribes to it;
	// params may carry secret:// references resolved at the last mile.
	TopicGatewayOperationsInvoke = "v1/gateway/operations/invoke"

	// TopicGatewayOperationsResult accepts the edge-reported operation
	// result (Edge -> NL), correlated by operation task id.
	TopicGatewayOperationsResult = "v1/gateway/operations/result"

	// FilterGatewayOperations is the single downlink filter for operation
	// invocation pushes.
	FilterGatewayOperations = "v1/gateway/operations/#"
)
