// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package gatewayapi implements the user-facing REST and WebSocket entry
// point. M0 exposes the WebSocket subscription protocol and a health probe;
// later milestones add authenticated REST use cases, all of which are
// documented in docs/api/openapi.yaml.
//
// Real-time fanout is constructed from Core NATS subscriptions: JetStream
// publishes are simultaneously ordinary Core publishes, so this service does
// not introduce a second fanout subject.
package gatewayapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/deviceevent"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/modelinventory"
	"github.com/nereidai/nereidlink/internal/ota"
	"github.com/nereidai/nereidlink/internal/probe"
	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/secretbinding"
	"github.com/nereidai/nereidlink/internal/stepup"
	"github.com/nereidai/nereidlink/internal/streams"
	"github.com/nereidai/nereidlink/internal/usermgmt"
)

// Tuning holds WebSocket transport settings supplied by configuration.
type Tuning struct {
	// ReadLimit caps inbound control frame sizes.
	ReadLimit int64

	// PingInterval controls application-level liveness pings.
	PingInterval time.Duration
}

// subscribeRequest is the client control frame for adding a subscription.
type subscribeRequest struct {
	Op       string `json:"op"`
	ID       string `json:"id"`
	Resource struct {
		// Type is the resource identifier.
		Type string `json:"type"`

		// Tenant scopes the subscription. It is required for principals that
		// hold more than one tenant; a single-tenant client may omit it and
		// has its only tenant implied. The server never trusts a tenant the
		// principal has no grant for.
		Tenant string `json:"tenant"`

		// Devices lists explicit device ids. An empty list requests every
		// device the caller is allowed to see: an unrestricted tenant member
		// gets a true wildcard, while a customer/group-scoped member gets the
		// wildcard materialized to its visible device set.
		Devices []string `json:"devices"`
	} `json:"resource"`
}

// dataFrame is the server-to-client real-time message.
type dataFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"`
	Data any    `json:"data"`
}

// Hub maintains one shared Core NATS subscription and routes decoded points
// to connected clients that subscribed to the originating device.
type Hub struct {
	nc   *nats.Conn
	tune Tuning

	// commands dispatches device commands for the REST use.
	commands CommandSender

	// commandCatalog manages the per-device command whitelist.
	commandCatalog CommandCatalogAdmin

	// batchCommands creates and queries asynchronous batch tasks.
	batchCommands BatchCommandService

	// probes creates and queries on-demand gateway probe tasks.
	probes ProbeService

	// ota creates and queries firmware OTA tasks.
	ota OTAService

	// blobs stores and presigns platform-hosted binary objects
	// (firmware packages). It may be nil when object storage is disabled.
	blobs BlobStore

	// blobSignKey authenticates local-backend download URLs; it is the
	// platform JWT secret and stays empty for an S3-only deployment.
	blobSignKey string

	// firmware manages the platform-hosted firmware repository (ADR-0006b).
	// It may be nil when object storage is disabled.
	firmware FirmwareService

	// ruleTools exposes node debug and dry-run capabilities.
	ruleTools RuleTools

	// alarmAdmin manages the alarm center list and acknowledgment.
	alarmAdmin AlarmAdmin

	// registryAdmin manages profiles, devices and credentials.
	registryAdmin RegistryAdmin

	// telemetryHistory queries stored telemetry.
	telemetryHistory TelemetryHistory

	// telemetryKeys discovers the measurement keys of a device.
	telemetryKeys TelemetryKeys

	// presenceLister lists online devices for the presence snapshot.
	presenceLister PresenceLister

	// telemetrySeries returns downsampled window series.
	telemetrySeries TelemetrySeries

	// commandHistory queries stored commands.
	commandHistory CommandHistory

	// deviceEvents queries stored device domain events.
	deviceEvents DeviceEventReader

	// notificationInbox lists and updates in-app notifications.
	notificationInbox NotificationInbox

	// hookAdmin manages pre-storage hooks and the DLQ.
	hookAdmin HookAdmin

	// ruleChainAdmin manages chain versions.
	ruleChainAdmin RuleChainAdmin

	// businessAdmin manages declarative business templates and instances.
	businessAdmin BusinessAdmin

	// linkEpisodeReader lists the gateway link episode history.
	linkEpisodeReader LinkEpisodeReader

	// moduleAdmin manages optional southbound protocol modules.
	moduleAdmin ModuleAdmin

	// forwarderAdmin manages third-party data forwarding (ADR-0018).
	forwarderAdmin ForwarderAdmin

	// companionAdmin manages companion-subscription/v1 endpoint registration
	// and the generic connector's desired/read-back rows.
	companionAdmin CompanionAdmin

	// provisionAdmin manages the platform tenant lifecycle.
	provisionAdmin ProvisionAdmin

	// authService authenticates user requests.
	authService AuthService

	// oidcService performs external IdP single sign-on; nil disables SSO.
	oidcService OIDCService
	// oidcFrontendBase is where the browser is returned after the callback.
	oidcFrontendBase string

	// ssoService performs NereidSphere one-time handoff ticket SSO; nil disables SSO.
	ssoService SSOService
	// ssoFrontendBase is where the browser is returned after the ticket callback.
	ssoFrontendBase string
	// ssoCallbackPath is the NS-registered browser redirect path (e.g.
	// /auth/ns-callback); empty when SSO is disabled.
	ssoCallbackPath string

	// nsIntegration backs the NS admin console integration endpoints; nil
	// leaves /v1/integration/ns/tenants unregistered.
	nsIntegration NSIntegration

	// captcha issues and verifies the login arithmetic challenge. It is
	// the in-process store by default; cluster mode replaces it with a
	// NATS-KV-backed instance so a challenge issued by one replica
	// verifies on another (SetCaptchaStore).
	captcha captchaChallenger

	// identityService verifies tokens for the RBAC layer.
	identityService *identity.Service

	// rbacResolver loads membership grants.
	rbacResolver *rbac.Resolver

	// frameDecoder transparently decompresses telemetry bus frames
	// (ADR-0003 compression). Without it every compressed envelope fails
	// protobuf unmarshal and real-time frames silently die here while the
	// persistence consumer (which has its own decoder) stores them fine.
	frameDecoder FrameDecoder

	// auditReader serves audit query and verification.
	auditReader AuditReader

	// trustAnchorManager manages platform CAs and the public bundle.
	trustAnchorManager TrustAnchorManager

	// memberAdmin manages tenant user accounts.
	memberAdmin MemberAdmin

	// dashboardAdmin persists user-defined dashboards.
	dashboardAdmin DashboardAdmin

	// escalationAdmin persists alarm escalation chains.
	escalationAdmin EscalationAdmin

	// connectorAdmin persists edge connector desired specs (ADR-0026).
	connectorAdmin ConnectorAdmin

	// connectorSchemas lists edge-registered connector config schemas.
	connectorSchemas ConnectorSchemaReader

	// protocolCatalog is the global, operator-editable device-service
	// catalog (ADR-0033).
	protocolCatalog ProtocolCatalogAdmin

	// edgeProfiles lists cached edge profiles and adopts them into native
	// device profiles.
	edgeProfiles EdgeProfileAdmin

	// subscriptionQuota checks WS subscription quotas.
	subscriptionQuota SubscriptionQuota

	// quotaAdmin persists tenant soft quotas.
	quotaAdmin QuotaAdmin

	// retentionStore persists per-tenant retention/legal-hold policy.
	retentionStore RetentionStore

	// discoveryPolicy manages per-tenant/per-gateway auto-provision policy.
	discoveryPolicy DiscoveryPolicyAdmin

	// discoveryApprovals lists and decides the human-review queue
	// (migration 0046).
	discoveryApprovals DiscoveryApprovalAdmin

	// secretStore backs secret:// references in connector config
	// (migration 0047); values are write-only over REST.
	secretStore SecretStore

	// secretValidator validates typed documents against the catalog.
	secretValidator DocumentValidator

	// secretTypes is the edge secret type descriptor catalog
	// (ADR-0034, migration 0067).
	secretTypes SecretTypeAdmin

	// secretBindingAdmin persists desired secret delivery bindings
	// (ADR-0034, migration 0071).
	secretBindingAdmin SecretBindingAdmin

	// profileDeliveryAdmin manages device-profile downlink targets and
	// delivery state (ADR-0038, migration 0085).
	profileDeliveryAdmin ProfileDeliveryAdmin

	// modelInventory lists a gateway's reported edge model inventory
	// (ADR-0039, migration 0086).
	modelInventory ModelInventoryReader

	// secretDeletion guards vault deletion against delivered copies and
	// executes forced deletes (ADR-0034, migration 0073).
	secretDeletion SecretDeletionAdmin

	// stepUp issues and consumes operation-time TOTP step-up challenges
	// (ADR-0035, migration 0068).
	stepUp *stepup.Service

	// operations is the gateway operation descriptor catalog
	// (ADR-0035, migration 0070).
	operations OperationCatalog

	// operationTasks executes operations and serves task history
	// (ADR-0035, migration 0072).
	operationTasks OperationInvoker

	// machineAdmin manages ADR-0021 OAuth2 service accounts.
	machineAdmin MachineAuthAdmin

	// machineGranter authenticates client credentials at the public
	// token endpoint.
	machineGranter MachineTokenGranter

	// apiKeyAdmin manages user-scoped long-lived API keys.
	apiKeyAdmin APIKeyAdmin

	// apiKeyAuthenticator verifies inbound X-Api-Key credentials.
	apiKeyAuthenticator APIKeyAuthenticator

	// opsCenter manages high-risk operation orders (ADR-0023).
	opsCenter OpsCenter

	// deadmanProbe reaches the independent platform fallback channel
	// (ADR-0019) for operator-triggered delivery tests.
	deadmanProbe DeadmanProbe

	// accounting answers ADR-0025 Top-N diagnostic queries.
	accounting AccountingReader

	// overviewReader aggregates tenant dashboard snapshots.
	overviewReader OverviewReader

	// statsReader collects system and business statistics.
	statsReader StatsReader

	// licenseAdmin evaluates licenses, usage and suspension.
	licenseAdmin LicenseAdmin

	// shadowStore manages desired and reported device state.
	shadowStore ShadowStore

	// ownershipAdmin manages customers, groups and device lifecycle.
	ownershipAdmin OwnershipAdmin

	// metricsHandler serves Prometheus exposition when wired.
	metricsHandler http.Handler

	// idempotency replays Idempotency-Key writes (ADR-0017).
	idempotency *idempotencyStore

	// deviceIngest serves token-authenticated device HTTP endpoints.
	deviceIngest http.Handler

	// instrument wraps the whole mux with request metrics when set.
	instrument func(http.Handler) http.Handler

	mu      sync.Mutex
	clients map[*Client]struct{}
}

// SetDeviceIngest wires the southbound HTTP device handler.
func (h *Hub) SetDeviceIngest(handler http.Handler) {
	h.deviceIngest = handler
}

// SetStatsReader wires the statistics service.
func (h *Hub) SetStatsReader(reader StatsReader) {
	h.statsReader = reader
}

// SetMetricsHandler wires the Prometheus /metrics handler.
func (h *Hub) SetMetricsHandler(handler http.Handler, instrumenter func(http.Handler) http.Handler) {
	h.metricsHandler = handler
	h.instrument = instrumenter
}

// SetCommands wires the command sender after hub construction.
func (h *Hub) SetCommands(sender CommandSender) {
	h.commands = sender
}

// SetCommandCatalog wires the command whitelist management service.
func (h *Hub) SetCommandCatalog(admin CommandCatalogAdmin) {
	h.commandCatalog = admin
}

// SetRuleTools wires the rule debug surface.
func (h *Hub) SetRuleTools(tools RuleTools) {
	h.ruleTools = tools
}

// SetRegistryAdmin wires the registry management service.
func (h *Hub) SetRegistryAdmin(admin RegistryAdmin) {
	h.registryAdmin = admin
}

// SetIdentity wires the token verifier and RBAC principal resolver,
// shared by the REST middleware and the WebSocket handshake.
func (h *Hub) SetIdentity(verifier *identity.Service, resolver *rbac.Resolver) {
	h.identityService = verifier
	h.rbacResolver = resolver
}

// FrameDecoder decompresses a bus message produced by buscodec.Codec.
type FrameDecoder interface {
	// Decode returns the original envelope bytes.
	Decode(raw []byte) ([]byte, error)
}

// SetFrameDecoder wires transparent bus decompression for real-time
// routing. It must be called with the same codec instance as the ingress
// SetEncoder whenever bus compression is enabled, otherwise compressed
// telemetry envelopes reach only the persistence path and never the
// WebSocket fanout. Nil (the default) is passthrough for compression off.
func (h *Hub) SetFrameDecoder(decoder FrameDecoder) {
	h.frameDecoder = decoder
}

// NewHub constructs the real-time hub.
func NewHub(nc *nats.Conn, tune Tuning) *Hub {
	return &Hub{
		nc:          nc,
		tune:        tune,
		clients:     map[*Client]struct{}{},
		idempotency: newIdempotencyStore(),
		captcha:     newCaptchaStore(),
	}
}

// presenceEvent mirrors presence.Event on the Core NATS channel.
type presenceEvent struct {
	Time      string `json:"time"`
	DeviceID  string `json:"device_id"`
	GatewayID string `json:"gateway_id"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	SessionID string `json:"session_id"`
}

// Run subscribes to the telemetry and presence channels, routing messages
// until ctx is canceled.
func (h *Hub) Run(ctx context.Context) error {
	telemetrySub, err := h.nc.Subscribe(streams.TelemetryWildcard(), func(msg *nats.Msg) {
		// Layout nl.telemetry.s<shard>.<tenant>.<part>: tokens
		// nl(1).telemetry(2).s<shard>(3).<tenant>(4).<part>(5).
		h.routeTelemetry(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = telemetrySub.Unsubscribe() }()

	presenceSub, err := h.nc.Subscribe(PresenceWildcard, func(msg *nats.Msg) {
		// Layout nl.presence.<tenant>.<device>: the tenant is token 3.
		h.routePresence(subjectToken(msg.Subject, 3), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = presenceSub.Unsubscribe() }()

	alarmSub, err := h.nc.Subscribe(AlarmWildcard, func(msg *nats.Msg) {
		// Layout nl.alarm.<tenant>.<device>: the tenant is token 3.
		h.routeAlarm(subjectToken(msg.Subject, 3), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = alarmSub.Unsubscribe() }()

	notificationSub, err := h.nc.Subscribe(NotificationWildcard, func(msg *nats.Msg) {
		// Layout nl.notification.<tenant>: the tenant is token 3.
		h.routeNotification(subjectToken(msg.Subject, 3), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = notificationSub.Unsubscribe() }()

	commandTaskSub, err := h.nc.Subscribe(CommandTaskWildcard, func(msg *nats.Msg) {
		// Layout nl.command.task.<tenant>: the tenant is token 4.
		h.routeCommandTask(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = commandTaskSub.Unsubscribe() }()

	probeTaskSub, err := h.nc.Subscribe(ProbeTaskWildcard, func(msg *nats.Msg) {
		// Layout nl.probe.task.<tenant>: the tenant is token 4.
		h.routeProbeTask(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = probeTaskSub.Unsubscribe() }()

	otaTaskSub, err := h.nc.Subscribe(OTATaskWildcard, func(msg *nats.Msg) {
		// Layout nl.ota.task.<tenant>: the tenant is token 4.
		h.routeOTATask(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = otaTaskSub.Unsubscribe() }()

	deviceEventSub, err := h.nc.Subscribe(deviceevent.Wildcard, func(msg *nats.Msg) {
		// Layout nl.deviceevent.<tenant>.<device>: the tenant is token 3.
		h.routeDeviceEvent(subjectToken(msg.Subject, 3), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = deviceEventSub.Unsubscribe() }()

	connectorSub, err := h.nc.Subscribe(connector.EventWildcard, func(msg *nats.Msg) {
		// Layout nl.connector.event.<tenant>: the tenant is token 4.
		h.routeConnector(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = connectorSub.Unsubscribe() }()

	modelSub, err := h.nc.Subscribe(modelinventory.EventWildcard, func(msg *nats.Msg) {
		// Layout nl.model.event.<tenant>: the tenant is token 4.
		h.routeModelInventory(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = modelSub.Unsubscribe() }()

	secretBindingSub, err := h.nc.Subscribe(SecretEventWildcard, func(msg *nats.Msg) {
		// Layout nl.secret.event.<tenant>: the tenant is token 4.
		h.routeSecretBinding(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = secretBindingSub.Unsubscribe() }()

	memberSub, err := h.nc.Subscribe(usermgmt.EventWildcard, func(msg *nats.Msg) {
		// Layout nl.member.event.<tenant>: the tenant is token 4.
		h.routeMember(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = memberSub.Unsubscribe() }()

	attributeSub, err := h.nc.Subscribe(AttributeWildcard, func(msg *nats.Msg) {
		// Layout nl.attribute.event.<tenant>.<device>: tenant token 4.
		h.routeAttribute(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = attributeSub.Unsubscribe() }()

	shadowSub, err := h.nc.Subscribe(ShadowWildcard, func(msg *nats.Msg) {
		// Layout nl.shadow.event.<tenant>.<device>: tenant token 4.
		h.routeShadow(subjectToken(msg.Subject, 4), msg.Data)
	})
	if err != nil {
		return err
	}
	defer func() { _ = shadowSub.Unsubscribe() }()

	<-ctx.Done()
	return nil
}

// subjectToken returns the 1-based token of a NATS subject, or "" when the
// subject is shorter than requested. The tenant segment of every real-time
// subject is read from here (never from the message payload) so a publisher
// cannot forge a tenant id to cross the fanout boundary.
func subjectToken(subject string, index int) string {
	start := 0
	for token := 1; token < index; token++ {
		next := strings.IndexByte(subject[start:], '.')
		if next < 0 {
			return ""
		}
		start += next + 1
	}
	end := strings.IndexByte(subject[start:], '.')
	if end < 0 {
		return subject[start:]
	}
	return subject[start : start+end]
}

// routeTelemetry decodes an Envelope/TelemetryPoint and forwards it to
// interested clients. Decode failures are counted/diagnosed by telemetry
// tooling later rather than breaking the fanout path.
func (h *Hub) routeTelemetry(tenant string, raw []byte) {
	// The bus may carry compressed envelopes (ADR-0003); the persistence
	// consumer decodes with the shared buscodec and this router must too.
	if h.frameDecoder != nil {
		decoded, err := h.frameDecoder.Decode(raw)
		if err != nil {
			slog.Warn("ws route envelope decode failed", "error", err)
			return
		}
		raw = decoded
	}
	var env v1.Envelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		slog.Warn("ws route envelope decode failed", "error", err)
		return
	}
	var point v1.TelemetryPoint
	if err := proto.Unmarshal(env.Payload, &point); err != nil {
		slog.Warn("ws route point decode failed", "error", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourceTelemetry, tenant, env.DeviceId) &&
			!client.wantsWildcard(ResourceTelemetry, tenant) {
			continue
		}
		data := map[string]any{
			"time":               time.UnixMilli(env.EventTime).UTC().Format(time.RFC3339),
			"device_id":          env.DeviceId,
			"measurement_key":    point.MeasurementKey,
			"v_double":           point.VDouble,
			"quality":            point.Quality,
			"raw_value":          point.RawDouble,
			"unit":               point.Unit,
			"descriptor_version": point.DescriptorVersion,
			"v_string":           point.VString,
			"value_type":         telemetryValueType(point.ValueType),
		}
		// Only boolean points carry v_bool; including the proto zero value
		// would falsely expose "v_bool":false on numeric points.
		if point.ValueType == v1.ValueType_VALUE_TYPE_BOOLEAN {
			data["v_bool"] = point.VBool
		}
		client.send(dataFrame{
			Type: ResourceTelemetry,
			ID:   "",
			Seq:  client.nextSeq(),
			Data: data,
		})
	}
}

// telemetryValueType maps the proto enum onto the wire string used by
// clients: number, boolean or string. Unspecified is treated as number
// for points produced before type tagging.
func telemetryValueType(value v1.ValueType) string {
	switch value {
	case v1.ValueType_VALUE_TYPE_BOOLEAN:
		return "boolean"
	case v1.ValueType_VALUE_TYPE_STRING:
		return "string"
	default:
		return "number"
	}
}

// routePresence forwards a decoded presence event to clients subscribed
// to the device.
func (h *Hub) routePresence(tenant string, raw []byte) {
	var event presenceEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("presence frame decode failed", "error", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourcePresence, tenant, event.DeviceID) &&
			!client.wantsWildcard(ResourcePresence, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourcePresence,
			Seq:  client.nextSeq(),
			Data: event,
		})
	}
}

// routeAlarm forwards a raw alarm event to subscribed clients. The event is
// delivered verbatim, decoded by the client against the documented shape.
func (h *Hub) routeAlarm(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("alarm frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourceAlarm, tenant, event.DeviceID) &&
			!client.wantsWildcard(ResourceAlarm, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceAlarm,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeDeviceEvent forwards a device domain event to subscribed clients,
// applying the same per-device data-scope matching as alarms: an explicit
// device subscription matches only that device, and a wildcard only reaches a
// client whose scope was materialized server-side.
func (h *Hub) routeDeviceEvent(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("device event frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourceDeviceEvent, tenant, event.DeviceID) &&
			!client.wantsWildcard(ResourceDeviceEvent, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceDeviceEvent,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeAttribute forwards one device attribute state change to clients
// subscribed to that device (or wildcarding it within data scope). The frame
// is forwarded verbatim, like device events.
func (h *Hub) routeAttribute(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("attribute frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourceAttribute, tenant, event.DeviceID) &&
			!client.wantsWildcard(ResourceAttribute, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceAttribute,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeShadow forwards one complete post-commit device shadow to clients
// subscribed to that device (or wildcarding it within data scope). Clients
// replace their whole shadow view with the forwarded frame.
func (h *Hub) routeShadow(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("shadow frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wants(ResourceShadow, tenant, event.DeviceID) &&
			!client.wantsWildcard(ResourceShadow, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceShadow,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeNotification forwards an in-app notification to every client with a
// notification subscription. Unlike device resources it carries no device
// filter: notifications are tenant-scoped, and Dev mode runs one tenant.
func (h *Hub) routeNotification(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("notification frame decode failed", "error", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceNotification, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceNotification,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeCommandTask forwards batch task progress to subscribed clients.
func (h *Hub) routeCommandTask(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event command.TaskEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("batch command frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceCommandTask, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceCommandTask,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeProbeTask forwards on-demand probe task progress to subscribed
// clients. The frame shape mirrors batch command progress (type task/item).
func (h *Hub) routeProbeTask(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event probe.TaskEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("probe task frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceProbeTask, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceProbeTask,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeOTATask forwards firmware OTA task progress to subscribed clients.
func (h *Hub) routeOTATask(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event ota.TaskEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("ota task frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceOTATask, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceOTATask,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeConnector forwards edge connector lifecycle events (desired writes,
// edge status and manifest reconciliation) to subscribed console clients.
// It is tenant-scoped: every client subscribed to the tenant's connector
// stream receives each event and re-reads the affected gateway itself, so no
// per-device scoping is applied here.
func (h *Hub) routeConnector(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event connector.EventFrame
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("connector event frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceConnector, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceConnector,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeModelInventory fans an edge model-inventory change out to clients
// subscribed to the tenant's model_inventory stream (ADR-0039). The frame is
// a change notification only; clients re-fetch the gateway's model list.
func (h *Hub) routeModelInventory(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("model inventory event frame decode failed", "error", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceModelInventory, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceModelInventory,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeSecretBinding forwards secret delivery lifecycle events (desired
// writes, withdrawals, edge status) to subscribed console clients. It is
// tenant-scoped: every client subscribed to the tenant's secret binding
// stream re-reads the affected gateway itself.
func (h *Hub) routeSecretBinding(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event secretbinding.EventFrame
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("secret binding event frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceSecretBinding, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceSecretBinding,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// routeMember forwards tenant member administration change notifications
// to subscribed console clients. It is tenant-scoped and platform-admin
// gated at subscription time: every subscribed client re-reads the
// tenant's member list itself rather than patching from the frame.
func (h *Hub) routeMember(tenant string, raw []byte) {
	if tenant == "" {
		return
	}
	var event usermgmt.EventFrame
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("member event frame decode failed", "error", err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if !client.wantsTenant(ResourceMember, tenant) {
			continue
		}
		client.send(dataFrame{
			Type: ResourceMember,
			Seq:  client.nextSeq(),
			Data: payload,
		})
	}
}

// Handler returns the HTTP handler exposing the WS endpoint and the health
// probe. The returned mux is the only HTTP surface mounted by gateway-api.
func (h *Hub) Handler(wsPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /v1/auth/captcha", h.handleGetCaptcha)
	mux.HandleFunc("GET /v1/auth/bootstrap-status", h.handleBootstrapStatus)
	mux.HandleFunc("POST /v1/auth/bootstrap", h.handleBootstrap)
	mux.HandleFunc("POST /v1/auth/login", h.handleLogin)
	mux.HandleFunc("POST /v1/auth/refresh", h.handleRefresh)
	mux.HandleFunc("POST /v1/auth/logout", h.handleLogout)
	mux.HandleFunc("POST /v1/auth/mfa/verify", h.handleMFAVerify)
	mux.HandleFunc("GET /v1/auth/mfa/status", h.handleMFAStatus)
	mux.HandleFunc("POST /v1/auth/mfa/totp/setup", h.handleTOTPSetup)
	mux.HandleFunc("POST /v1/auth/mfa/totp/confirm", h.handleTOTPConfirm)
	mux.HandleFunc("DELETE /v1/auth/mfa/totp", h.handleTOTPDisable)
	mux.HandleFunc("GET /v1/auth/me", h.handleCurrent)
	mux.HandleFunc("GET /v1/auth/sessions", h.handleListSessions)
	mux.HandleFunc("DELETE /v1/auth/sessions/{session}", h.handleRevokeSession)
	mux.HandleFunc("GET /v1/auth/oidc/status", h.handleOIDCStatus)
	mux.HandleFunc("GET /v1/auth/oidc/login", h.handleOIDCLogin)
	mux.HandleFunc("GET /v1/auth/oidc/callback", h.handleOIDCCallback)
	mux.HandleFunc("GET /v1/auth/sso/status", h.handleSSOStatus)
	if h.ssoCallbackPath != "" {
		// The path is registered in NS as this product's sso_callback_path
		// (commonly /auth/ns-callback); NS redirects the browser here with
		// ?ticket=...&to=....
		mux.HandleFunc("GET "+h.ssoCallbackPath, h.handleSSOTicket)
	}
	if h.nsIntegration != nil {
		mux.HandleFunc("GET /v1/integration/ns/tenants", h.handleNSIntegrationTenants)
	}
	if h.metricsHandler != nil {
		mux.Handle("GET /metrics", h.metricsHandler)
	}
	if h.deviceIngest != nil {
		mux.Handle("/v1/me/", h.deviceIngest)
	}
	mux.HandleFunc("GET "+wsPath, func(w http.ResponseWriter, r *http.Request) {
		h.serveWS(r.Context(), w, r)
	})
	mux.HandleFunc("GET /v1/tenants/{tenant}/stats/system",
		h.handleSystemStats)
	mux.HandleFunc("GET /v1/platform/accounting/topn",
		h.handleAccountingTopN)
	mux.HandleFunc("GET /v1/tenants/{tenant}/stats/business",
		h.handleBusinessStats)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/commands",
		h.handleSendCommand)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/command-catalog",
		h.handleListCommandCatalog)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/command-catalog",
		h.handleCreateCommandCatalog)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/devices/{device}/command-catalog/{command}",
		h.handleUpdateCommandCatalog)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/devices/{device}/command-catalog/{command}",
		h.handleDeleteCommandCatalog)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/shadow",
		h.handleGetShadow)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/devices/{device}/shadow/desired",
		h.handleUpdateDesired)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/shadow/report",
		h.handleUpdateReported)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/shadow/desired-schema",
		h.handleShadowDesiredSchema)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/commands",
		h.handleCommandHistory)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/ota-items",
		h.handleListDeviceOTAItems)
	mux.HandleFunc("GET /v1/tenants/{tenant}/commands",
		h.handleTenantCommands)
	mux.HandleFunc("POST /v1/tenants/{tenant}/command-tasks",
		h.idempotency.withIdempotency("rest", h.handleCreateBatchCommand))
	mux.HandleFunc("GET /v1/tenants/{tenant}/command-tasks",
		h.handleListBatchCommands)
	mux.HandleFunc("GET /v1/tenants/{tenant}/command-tasks/{task}",
		h.handleGetBatchCommand)
	mux.HandleFunc("GET /v1/tenants/{tenant}/command-tasks/{task}/items",
		h.handleListBatchItems)
	mux.HandleFunc("POST /v1/tenants/{tenant}/probes",
		h.idempotency.withIdempotency("rest", h.handleCreateProbe))
	mux.HandleFunc("GET /v1/tenants/{tenant}/probes",
		h.handleListProbes)
	mux.HandleFunc("GET /v1/tenants/{tenant}/probes/{task}",
		h.handleGetProbe)
	mux.HandleFunc("GET /v1/tenants/{tenant}/probes/{task}/items",
		h.handleListProbeItems)
	mux.HandleFunc("POST /v1/tenants/{tenant}/ota-tasks",
		h.idempotency.withIdempotency("rest", h.handleCreateOTA))
	mux.HandleFunc("GET /v1/tenants/{tenant}/ota-tasks",
		h.handleListOTAs)
	mux.HandleFunc("GET /v1/tenants/{tenant}/ota-tasks/{task}",
		h.handleGetOTA)
	mux.HandleFunc("GET /v1/tenants/{tenant}/ota-tasks/{task}/items",
		h.handleListOTAItems)
	mux.HandleFunc("GET /v1/tenants/{tenant}/ota-tasks/{task}/events",
		h.handleListOTAEvents)
	mux.HandleFunc("POST /v1/tenants/{tenant}/ota-tasks/{task}/cancel",
		h.handleCancelOTA)
	mux.HandleFunc("POST /v1/tenants/{tenant}/ota-tasks/{task}/pause",
		h.handlePauseOTA)
	mux.HandleFunc("POST /v1/tenants/{tenant}/ota-tasks/{task}/resume",
		h.handleResumeOTA)

	// Platform-hosted firmware repository (ADR-0006b).
	mux.HandleFunc("/v1/tenants/{tenant}/firmware",
		h.handleFirmware)
	mux.HandleFunc("/v1/tenants/{tenant}/firmware/{firmware}",
		h.handleFirmwareOne)
	mux.HandleFunc("POST /v1/tenants/{tenant}/firmware/{firmware}/stable",
		h.handleFirmwareStable)
	mux.HandleFunc("GET /v1/tenants/{tenant}/firmware/{firmware}/download",
		h.handleFirmwareDownload)

	// Self-served downloads for the local blob backend (ADR-0006): the
	// HMAC query parameters are the request's own credentials.
	mux.HandleFunc("GET /v1/blobs/get", h.handleGetLocalBlob)

	// Resolve a device media blob reference into a short-lived signed URL
	// (device:read); used by console telemetry views for blob:// values.
	mux.HandleFunc("GET /v1/tenants/{tenant}/blob/sign", h.handleSignMediaBlob)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/telemetry",
		h.handleTelemetryHistory)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/telemetry/keys",
		h.handleTelemetryKeys)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/telemetry/series",
		h.handleTelemetrySeries)
	mux.HandleFunc("GET /v1/tenants/{tenant}/presence",
		h.handlePresenceSnapshot)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/attributes",
		h.handleListAttributes)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/devices/{device}/attributes",
		h.handlePutAttribute)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/devices/{device}/attributes",
		h.handleDeleteAttribute)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/events",
		h.handleDeviceEvents)
	mux.HandleFunc("GET /v1/tenants/{tenant}/device-events",
		h.handleDeviceEvents)
	mux.HandleFunc("/v1/tenants/{tenant}/storage-hooks",
		h.handleStorageHooks)
	mux.HandleFunc("/v1/tenants/{tenant}/storage-hooks/{hook}",
		h.handleStorageHook)
	mux.HandleFunc("GET /v1/tenants/{tenant}/telemetry-dlq",
		h.handleDLQ)
	mux.HandleFunc("POST /v1/tenants/{tenant}/telemetry-dlq/delete",
		h.handleDLQDelete)
	mux.HandleFunc("POST /v1/tenants/{tenant}/telemetry-dlq/replay",
		h.handleDLQReplay)
	mux.HandleFunc("GET /v1/tenants/{tenant}/notifications",
		h.handleListNotifications)
	mux.HandleFunc("POST /v1/tenants/{tenant}/notifications/read",
		h.handleMarkNotificationsRead)
	mux.HandleFunc("GET /v1/tenants/{tenant}/overview",
		h.handleOverview)
	mux.HandleFunc("/v1/tenants/{tenant}/rule-chain-groups",
		h.handleRuleChainGroups)
	mux.HandleFunc("/v1/tenants/{tenant}/rule-chain-groups/{group}",
		h.handleRuleChainGroup)
	mux.HandleFunc("/v1/tenants/{tenant}/rule-chain-groups/{group}/versions",
		h.handleRuleChainVersions)
	mux.HandleFunc("/v1/tenants/{tenant}/business-templates",
		h.handleBusinessTemplates)
	mux.HandleFunc("POST /v1/tenants/{tenant}/business-templates/import",
		h.handleBusinessTemplateImport)
	mux.HandleFunc("GET /v1/tenants/{tenant}/business-templates/export",
		h.handleBusinessTemplateExport)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/business-templates/{template}/versions/{version}",
		h.handleBusinessTemplateVersion)
	mux.HandleFunc("/v1/tenants/{tenant}/business-instances",
		h.handleBusinessInstances)
	mux.HandleFunc("/v1/tenants/{tenant}/business-instances/{instance}",
		h.handleBusinessInstance)
	mux.HandleFunc("POST /v1/tenants/{tenant}/business-instances/{instance}/validate",
		h.handleBusinessValidate)
	mux.HandleFunc("GET /v1/tenants/{tenant}/business-instances/{instance}/expansion",
		h.handleBusinessExpansion)
	mux.HandleFunc("POST /v1/tenants/{tenant}/business-instances/{instance}/deploy",
		h.handleBusinessDeploy)
	mux.HandleFunc("POST /v1/tenants/{tenant}/business-instances/{instance}/undeploy",
		h.handleBusinessUndeploy)
	mux.HandleFunc("POST /v1/tenants/{tenant}/business-instances/{instance}/rollback",
		h.handleBusinessRollback)
	mux.HandleFunc("GET /v1/tenants/{tenant}/business-instances/{instance}/monitor",
		h.handleBusinessMonitor)
	mux.HandleFunc("GET /v1/tenants/{tenant}/rule-chains/{chain}",
		h.handleRuleChain)
	mux.HandleFunc("GET /v1/tenants/{tenant}/link-episodes",
		h.handleLinkEpisodes)
	mux.HandleFunc("POST /v1/tenants/{tenant}/rule-chains/{chain}/activate",
		h.handleActivateChain)
	mux.HandleFunc("/v1/tenants/{tenant}/rule-nodes/{node}/debug",
		h.handleNodeDebug)
	mux.HandleFunc("POST /v1/tenants/{tenant}/rule-chains/dry-run",
		h.handleDryRun)
	mux.HandleFunc("GET /v1/tenants/{tenant}/rule-chains/trace",
		h.handleChainTrace)
	mux.HandleFunc("POST /v1/tenants/{tenant}/rule-chains/trace",
		h.handleChainTrace)
	mux.HandleFunc("GET /v1/tenants/{tenant}/alarms",
		h.handleAlarms)
	mux.HandleFunc("POST /v1/tenants/{tenant}/alarms/{alarm}/ack",
		h.handleAckAlarm)
	mux.HandleFunc("POST /v1/tenants/{tenant}/alarms/{alarm}/assign",
		h.handleAlarmAssign)
	mux.HandleFunc("/v1/tenants/{tenant}/alarms/{alarm}/comments",
		h.handleAlarmComments)
	mux.HandleFunc("/v1/tenants/{tenant}/dashboards",
		h.handleDashboards)
	mux.HandleFunc("/v1/tenants/{tenant}/dashboards/{dashboard}",
		h.handleDashboard)
	mux.HandleFunc("/v1/tenants/{tenant}/escalation-chains",
		h.handleEscalationChains)
	mux.HandleFunc("/v1/tenants/{tenant}/escalation-chains/{chain}",
		h.handleEscalationChain)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/connectors",
		h.handleConnectors)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/connectors-reconcile",
		h.handleConnectorReconcile)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/connectors/{spec}",
		h.handleConnector)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/models",
		h.handleGatewayModels)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/secret-bindings",
		h.handleSecretBindings)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/secret-bindings/{secret}",
		h.handleSecretBinding)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/profile-deliveries",
		h.handleGatewayProfileDeliveries)
	mux.HandleFunc("/v1/tenants/{tenant}/profiles/{profile}/targets",
		h.handleProfileTargets)
	mux.HandleFunc("/v1/tenants/{tenant}/profiles/{profile}/targets/{gateway}",
		h.handleProfileTarget)
	mux.HandleFunc("/v1/tenants/{tenant}/profiles/{profile}/deliveries",
		h.handleProfileDeliveries)
	mux.HandleFunc("POST /v1/tenants/{tenant}/gateways/{gateway}/operations/{operation}/invoke",
		h.handleOperationInvoke)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/operations",
		h.handleGatewayOperations)

	// Third-party data forwarding (ADR-0018).
	mux.HandleFunc("/v1/tenants/{tenant}/forwarders",
		h.handleForwarders)
	mux.HandleFunc("/v1/tenants/{tenant}/forwarders/{forwarder}",
		h.handleForwarder)

	// Generic companion connector (companion-subscription/v1).
	mux.HandleFunc("/v1/tenants/{tenant}/companion-endpoints",
		h.handleCompanionEndpoints)
	mux.HandleFunc("/v1/tenants/{tenant}/companion-endpoints/{endpoint}",
		h.handleCompanionEndpoint)
	mux.HandleFunc("GET /v1/tenants/{tenant}/companion-endpoints/{endpoint}/subscriptions",
		h.handleCompanionSubscriptions)
	mux.HandleFunc("GET /v1/tenants/{tenant}/forwarder-dlq",
		h.handleForwarderDLQ)
	mux.HandleFunc("GET /v1/tenants/{tenant}/forwarder-deliveries",
		h.handleForwarderDeliveries)
	mux.HandleFunc("POST /v1/tenants/{tenant}/forwarder-dlq/{dlq}/replay",
		h.handleForwarderReplay)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/operation-tasks",
		h.handleOperationTasks)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/operation-tasks/{task}",
		h.handleOperationTask)
	mux.HandleFunc("GET /v1/tenants/{tenant}/connector-schemas",
		h.handleConnectorSchemas)
	mux.HandleFunc("/v1/tenants/{tenant}/edge-secret-types",
		h.handleSecretTypes)
	mux.HandleFunc("/v1/tenants/{tenant}/edge-secret-types/{type}",
		h.handleSecretType)
	mux.HandleFunc("POST /v1/tenants/{tenant}/step-up/challenges",
		h.handleStepUpChallenges)
	mux.HandleFunc("POST /v1/tenants/{tenant}/step-up/challenges/{challenge}",
		h.handleStepUpChallenge)
	mux.HandleFunc("POST /v1/tenants/{tenant}/step-up/demo",
		h.handleStepUpDemo)
	mux.HandleFunc("/v1/tenants/{tenant}/gateway-operations",
		h.handleOperations)
	mux.HandleFunc("/v1/tenants/{tenant}/gateway-operations/{operation}",
		h.handleOperation)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/gateway-operations/{operation}/disabled",
		h.handleOperationDisabled)
	mux.HandleFunc("/v1/tenants/{tenant}/protocol-families",
		h.handleProtocolFamilies)
	mux.HandleFunc("/v1/tenants/{tenant}/protocol-families/{family}",
		h.handleProtocolFamily)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/edge-profiles",
		h.handleEdgeProfiles)
	mux.HandleFunc("GET /v1/tenants/{tenant}/gateways/{gateway}/edge-profiles/{profile}",
		h.handleEdgeProfileDetail)
	mux.HandleFunc("POST /v1/tenants/{tenant}/gateways/{gateway}/edge-profiles/{profile}/adopt",
		h.handleEdgeProfileAdopt)
	mux.HandleFunc("/v1/tenants/{tenant}/discovery-policy",
		h.handleDiscoveryTenantPolicy)
	mux.HandleFunc("/v1/tenants/{tenant}/gateways/{gateway}/discovery-policy",
		h.handleDiscoveryGatewayPolicy)
	mux.HandleFunc("GET /v1/tenants/{tenant}/discovery-candidates",
		h.handleDiscoveryCandidates)
	mux.HandleFunc("GET /v1/tenants/{tenant}/discovery-candidates/{candidate}",
		h.handleDiscoveryCandidate)
	mux.HandleFunc("POST /v1/tenants/{tenant}/discovery-candidates/{candidate}/approve",
		h.handleDiscoveryCandidateApprove)
	mux.HandleFunc("POST /v1/tenants/{tenant}/discovery-candidates/{candidate}/reject",
		h.handleDiscoveryCandidateReject)
	mux.HandleFunc("GET /v1/tenants/{tenant}/secrets",
		h.handleTenantSecrets)
	mux.HandleFunc("/v1/tenants/{tenant}/secrets/{secret}",
		h.handleTenantSecret)
	mux.HandleFunc("/v1/tenants/{tenant}/quota",
		h.handleQuota)
	mux.HandleFunc("GET /v1/tenants/{tenant}/retention",
		h.handleRetentionList)
	mux.HandleFunc("GET /v1/tenants/{tenant}/retention/{domain}",
		h.handleRetentionGet)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/retention/{domain}",
		h.handleRetentionPut)
	mux.HandleFunc("/v1/tenants/{tenant}/profiles",
		h.handleProfiles)
	mux.HandleFunc("/v1/tenants/{tenant}/profiles/{profile}",
		h.handleProfile)
	mux.HandleFunc("POST /v1/tenants/{tenant}/profiles/import/preview",
		h.handleProfileImportPreview)
	mux.HandleFunc("POST /v1/tenants/{tenant}/profiles/import",
		h.handleProfileImport)
	mux.HandleFunc("POST /v1/tenants/{tenant}/profiles/bulk-import",
		h.handleProfileBulkImport)
	mux.HandleFunc("POST /v1/tenants/{tenant}/profiles/bulk-import/preview",
		h.handleProfileBulkImportPreview)
	mux.HandleFunc("GET /v1/tenants/{tenant}/profiles/bulk-import/template",
		h.handleProfileBulkImportTemplate)
	mux.HandleFunc("/v1/tenants/{tenant}/devices",
		h.idempotency.withIdempotency("rest", h.handleDevices))
	mux.HandleFunc("/v1/tenants/{tenant}/devices/{device}",
		h.handleDevice)
	// Literal segments beat the {device} wildcard, so the bulk action is
	// never mistaken for a device whose id happens to be the last segment.
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/bulk-decommission",
		h.handleBulkDecommission)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/bulk-import",
		h.handleBulkImport)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/bulk-import/preview",
		h.handleBulkImportPreview)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/bulk-import/template",
		h.handleBulkImportTemplate)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/claim-token",
		h.handleClaimToken)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/claim",
		h.handleClaim)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/transfer",
		h.handleTransfer)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/decommission",
		h.handleDecommission)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/reactivate",
		h.handleReactivate)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/quarantine",
		h.handleQuarantine)
	// Reason codes are read by the UI before it renders the picker; the
	// literal segment keeps it from being parsed as a device id.
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/quarantine-reasons",
		h.handleQuarantineReasons)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/quarantine/release",
		h.handleQuarantineRelease)
	mux.HandleFunc("/v1/tenants/{tenant}/customers",
		h.handleCustomers)
	mux.HandleFunc("/v1/tenants/{tenant}/customer-device-counts",
		h.handleCustomerDeviceCounts)
	mux.HandleFunc("/v1/tenants/{tenant}/group-device-counts",
		h.handleGroupDeviceCounts)
	mux.HandleFunc("/v1/tenants/{tenant}/customers/{customer}",
		h.handleCustomer)
	mux.HandleFunc("/v1/tenants/{tenant}/device-groups",
		h.handleDeviceGroups)
	mux.HandleFunc("PUT /v1/tenants/{tenant}/device-groups/{group}",
		h.handleDeviceGroup)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/device-groups/{group}",
		h.handleDeviceGroup)
	mux.HandleFunc("/v1/tenants/{tenant}/devices/{device}/credentials",
		h.handleCredentials)
	mux.HandleFunc("/v1/tenants/{tenant}/devices/{device}/credentials/{credential}",
		h.handleCredential)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/credentials/{credential}/status",
		h.handleCredentialStatus)
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/credentials/{credential}/rotate",
		h.handleCredentialRotate)
	mux.HandleFunc("GET /v1/tenants/{tenant}/devices/{device}/credentials/{credential}/secret",
		h.handleCredentialSecret)
	// Literal segments beat the {credential} wildcard, so revoke-all
	// resolves to this route instead of a credential id.
	mux.HandleFunc("POST /v1/tenants/{tenant}/devices/{device}/credentials/revoke-all",
		h.handleCredentialRevokeAll)
	mux.HandleFunc("POST /v1/tenants/{tenant}/credential-epoch",
		h.handleCredentialEpoch)

	mux.HandleFunc("GET /v1/tenants/{tenant}/modules",
		h.handleListModules)
	mux.HandleFunc("POST /v1/tenants/{tenant}/modules",
		h.handleCreateModule)
	mux.HandleFunc("GET /v1/tenants/{tenant}/modules/{module}",
		h.handleGetModule)
	mux.HandleFunc("PATCH /v1/tenants/{tenant}/modules/{module}",
		h.handleUpdateModule)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/modules/{module}",
		h.handleDeleteModule)

	mux.HandleFunc("POST /v1/modules/register",
		h.handleModuleRegister)
	mux.HandleFunc("POST /v1/modules/{module}/heartbeat",
		h.handleModuleHeartbeat)

	mux.HandleFunc("GET /v1/audit", h.handleListAudit)
	mux.HandleFunc("POST /v1/audit/verify", h.handleVerifyAudit)
	mux.HandleFunc("/v1/platform/ca", h.handlePlatformCAs)
	mux.HandleFunc("POST /v1/platform/ca/{ca}/status", h.handlePlatformCAStatus)
	mux.HandleFunc("DELETE /v1/platform/ca/{ca}", h.handlePlatformCADelete)
	mux.HandleFunc("GET /v1/me/ca.pem", h.handlePublicCABundle)

	mux.HandleFunc("GET /v1/tenants/{tenant}/license",
		h.handleTenantLicenseState)
	mux.HandleFunc("POST /v1/tenants/{tenant}/suspend",
		h.handleSuspendTenant)
	mux.HandleFunc("POST /v1/tenants/{tenant}/resume",
		h.handleResumeTenant)
	mux.HandleFunc("GET /v1/usage", h.handleListUsage)
	mux.HandleFunc("GET /v1/tenants/{tenant}/usage",
		h.handleTenantUsage)

	// Deployment license status and administrator upload. Both are
	// platform-super-admin only; the upload verifies a signed deployment
	// (scope "*") envelope with the same key as the operations CLI.
	mux.HandleFunc("GET /v1/platform/license",
		h.handlePlatformLicense)
	mux.HandleFunc("PUT /v1/platform/license",
		h.handleInstallPlatformLicense)

	mux.HandleFunc("GET /v1/tenants", h.handleListTenants)
	mux.HandleFunc("POST /v1/tenants", h.handleCreateTenant)
	mux.HandleFunc("GET /v1/tenants/{tenant}", h.handleGetTenant)
	mux.HandleFunc("POST /v1/tenants/{tenant}/retry", h.handleRetryTenant)

	mux.HandleFunc("/v1/tenants/{tenant}/members",
		h.handleMembers)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/members/{user}",
		h.handleMember)
	mux.HandleFunc("POST /v1/tenants/{tenant}/members/{user}/roles",
		h.handleMemberRoles)
	mux.HandleFunc("POST /v1/tenants/{tenant}/members/{user}/data-scope",
		h.handleMemberDataScope)
	mux.HandleFunc("POST /v1/tenants/{tenant}/members/{user}/password",
		h.handleMemberPassword)
	mux.HandleFunc("POST /v1/tenants/{tenant}/members/{user}/enabled",
		h.handleMemberEnabled)

	// ADR-0021 machine identity: OAuth2 client_credentials service
	// accounts. Management is tenant scoped and guarded by the
	// serviceaccount:read/write points (tenant admin only).
	mux.HandleFunc("POST /v1/tenants/{tenant}/service-accounts",
		h.handleCreateServiceAccount)
	mux.HandleFunc("GET /v1/tenants/{tenant}/service-accounts",
		h.handleListServiceAccounts)
	mux.HandleFunc("GET /v1/tenants/{tenant}/service-accounts/{account}",
		h.handleGetServiceAccount)
	mux.HandleFunc("PATCH /v1/tenants/{tenant}/service-accounts/{account}",
		h.handleUpdateServiceAccount)
	mux.HandleFunc("POST /v1/tenants/{tenant}/service-accounts/{account}/enabled",
		h.handleSetServiceAccountEnabled)
	mux.HandleFunc("POST /v1/tenants/{tenant}/service-accounts/{account}/rotate-secret",
		h.handleRotateServiceAccountSecret)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/service-accounts/{account}",
		h.handleDeleteServiceAccount)

	// User-scoped long-lived API-key self-service. These account routes
	// require an interactive human session; X-Api-Key cannot mint keys.
	mux.HandleFunc("POST /v1/auth/api-keys", h.handleCreateAPIKey)
	mux.HandleFunc("GET /v1/auth/api-keys", h.handleListOwnAPIKeys)
	mux.HandleFunc("GET /v1/auth/api-keys/{key}", h.handleGetOwnAPIKey)
	mux.HandleFunc("POST /v1/auth/api-keys/{key}/rotate", h.handleRotateOwnAPIKey)
	mux.HandleFunc("POST /v1/auth/api-keys/{key}/revoke", h.handleRevokeOwnAPIKey)
	mux.HandleFunc("DELETE /v1/auth/api-keys/{key}", h.handleDeleteOwnAPIKey)

	// Tenant-wide administrator visibility and revocation.
	mux.HandleFunc("GET /v1/tenants/{tenant}/api-keys", h.handleListTenantAPIKeys)
	mux.HandleFunc("GET /v1/tenants/{tenant}/api-keys/{key}", h.handleGetTenantAPIKey)
	mux.HandleFunc("POST /v1/tenants/{tenant}/api-keys/{key}/revoke", h.handleRevokeTenantAPIKey)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/api-keys/{key}", h.handleDeleteTenantAPIKey)

	// The public OAuth2 token endpoint; the presented secret is the auth.
	mux.HandleFunc("POST /v1/auth/token", h.handleToken)

	// Web-side confirmation of CLI-initiated operation orders.
	h.RegisterOpsReview(mux)

	// Self-contained OpenAPI documentation UI and embedded spec. These
	// paths are outside /v1/ and therefore exempt from bearer auth, the same
	// trust model as the SPA on a private on-premises deployment.
	mux.HandleFunc("GET /api/docs", h.handleAPIDocs)
	mux.HandleFunc("GET /api/docs/", h.handleAPIDocsAsset)

	mux.HandleFunc("/", h.serveWeb)
	if h.instrument != nil {
		return traceMiddleware(h.authorize(h.instrument(mux), wsPath))
	}
	return traceMiddleware(h.authorize(mux, wsPath))
}

// serveWS upgrades a connection, registers the client and processes control
// frames until disconnection. Authentication precedes the upgrade: the
// browser WebSocket API cannot set headers, so the access token is sent
// as the access_token query parameter and a failure is a plain HTTP 401.
func (h *Hub) serveWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	claims, err := h.identityService.ParseAccessToken(
		ctx, r.URL.Query().Get("access_token"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	principal, err := h.rbacResolver.FromClaims(ctx, claims)
	if err != nil {
		http.Error(w, "authorization failed", http.StatusForbidden)
		return
	}
	singleTenant := ""
	if len(principal.Tenants) == 1 {
		for tenant := range principal.Tenants {
			singleTenant = tenant
		}
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(h.tune.ReadLimit)

	client := NewClient(conn, h.remove, h.tune.PingInterval)
	client.tenant = singleTenant
	h.add(client)
	defer func() {
		client.shutdown()
		h.remove(client)
		_ = conn.CloseNow()
	}()

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req subscribeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			client.sendError(ErrorCodeInvalidFrame, "invalid control frame")
			continue
		}
		if req.Op != OpSubscribe ||
			(req.Resource.Type != ResourceTelemetry &&
				req.Resource.Type != ResourcePresence &&
				req.Resource.Type != ResourceAlarm &&
				req.Resource.Type != ResourceNotification &&
				req.Resource.Type != ResourceCommandTask &&
				req.Resource.Type != ResourceProbeTask &&
				req.Resource.Type != ResourceOTATask &&
				req.Resource.Type != ResourceDeviceEvent &&
				req.Resource.Type != ResourceConnector &&
				req.Resource.Type != ResourceModelInventory &&
				req.Resource.Type != ResourceSecretBinding &&
				req.Resource.Type != ResourceAttribute &&
				req.Resource.Type != ResourceShadow &&
				req.Resource.Type != ResourceMember) {
			client.sendError(ErrorCodeUnsupportedSubscription, "unsupported subscription")
			continue
		}

		// Resolve the tenant the subscription is opened against. A
		// single-tenant principal has it implied; a multi-tenant principal
		// (platform admin) must name a tenant it is then authorized for.
		tenant := req.Resource.Tenant
		if tenant == "" {
			tenant = singleTenant
		}
		grant, grantOK := principal.Tenants[tenant]
		if tenant == "" || (!grantOK && !principal.PlatformAdmin) {
			client.sendError(ErrorCodeUnsupportedSubscription,
				"tenant is required or not accessible")
			continue
		}
		if !principal.HasPoint(tenant, subscriptionPoint(req.Resource.Type)) &&
			!principal.PlatformAdmin {
			client.sendError(ErrorCodeUnsupportedSubscription,
				"missing permission for subscription")
			continue
		}

		// Resolve the data scope once per frame, exactly as the REST
		// authorize layer does. A platform admin browsing a tenant it has no
		// membership in is treated as tenant-wide.
		scope := rbac.UnrestrictedScope(tenant)
		if grantOK && !principal.PlatformAdmin {
			resolved, err := h.rbacResolver.DataScopeFor(ctx, tenant, grant)
			if err != nil {
				client.sendError(ErrorCodeUnsupportedSubscription,
					"your data scope is no longer valid; ask an administrator")
				continue
			}
			scope = resolved
		}
		scopedCtx := rbac.ContextWithScope(ctx, scope)

		// Device-scoped resources are intersected with the visible device set.
		// An unrestricted member keeps a server-side wildcard; a customer/group
		// member has the wildcard materialized to its visible devices, so it
		// can never receive a frame for a device outside its scope. Tenant-wide
		// resources (notifications, task progress) are isolated per tenant but
		// carry no per-device filter.
		wildcard := !isDeviceScopedResource(req.Resource.Type)
		var allowedDevices []string
		if isDeviceScopedResource(req.Resource.Type) {
			visible, allVisible, err := h.visibleSubDevices(scopedCtx, tenant, req.Resource.Devices)
			if err != nil {
				client.sendError(ErrorCodeInvalidFrame, "invalid subscription")
				continue
			}
			if allVisible {
				wildcard = true
			} else {
				allowedDevices = visible
			}
		}

		if singleTenant != "" && !client.hasSubscription(req.ID) {
			if h.subscriptionQuota != nil {
				current := h.tenantSubscriptionCount(singleTenant)
				if err := h.subscriptionQuota.CheckSubscription(ctx, singleTenant, current); err != nil {
					client.sendError(ErrorCodeQuotaExceeded, err.Error())
					continue
				}
			}
		}
		client.subscribe(req.ID, req.Resource.Type, tenant, allowedDevices, wildcard)
	}
}

// isDeviceScopedResource reports whether a real-time resource is filtered per
// device under the caller's data scope. Notifications and task-progress
// streams are tenant-scoped resources and only undergo tenant isolation.
func isDeviceScopedResource(resourceType string) bool {
	switch resourceType {
	case ResourceTelemetry, ResourcePresence, ResourceAlarm,
		ResourceDeviceEvent, ResourceAttribute, ResourceShadow:
		return true
	default:
		return false
	}
}

// subscriptionPoint maps a WS resource to the permission point it requires.
func subscriptionPoint(resourceType string) string {
	switch resourceType {
	case ResourceTelemetry, ResourcePresence,
		ResourceCommandTask, ResourceOTATask, ResourceDeviceEvent,
		ResourceAttribute, ResourceShadow:
		return rbac.PointDeviceRead
	case ResourceAlarm:
		return rbac.PointAlarmRead
	case ResourceNotification:
		return rbac.PointNotificationRead
	case ResourceProbeTask:
		return rbac.PointProbeRead
	case ResourceConnector:
		return rbac.PointConnectorRead
	case ResourceModelInventory:
		// Edge model inventory shares the connector read scope (ADR-0039).
		return rbac.PointConnectorRead
	case ResourceSecretBinding:
		return rbac.PointEdgeSecretRead
	case ResourceMember:
		// Delegated member administration: a platform admin bypasses the
		// point check, while a TENANT_ADMIN subscribes in their own tenant.
		return rbac.PointMembersAdmin
	default:
		return ""
	}
}

// visibleSubDevices converts the registry visibility result into a flat id
// list for the subscription hot path.
func (h *Hub) visibleSubDevices(ctx context.Context, tenant string, requested []string) ([]string, bool, error) {
	if h.registryAdmin == nil {
		// Without a registry the gateway cannot narrow a scoped member; deny
		// rather than fall back to a tenant-wide wildcard.
		return nil, false, nil
	}
	visible, all, err := h.registryAdmin.VisibleDeviceIDs(ctx, tenant, requested)
	if err != nil {
		return nil, false, err
	}
	if all {
		return nil, true, nil
	}
	ids := make([]string, 0, len(visible))
	for id := range visible {
		ids = append(ids, id)
	}
	return ids, false, nil
}

// SubscriptionQuota checks the tenant WebSocket subscription quota.
type SubscriptionQuota interface {
	CheckSubscription(ctx context.Context, tenantID string, currentCount int) error
}

// SetSubscriptionQuota wires the WebSocket subscription quota checker.
func (h *Hub) SetSubscriptionQuota(checker SubscriptionQuota) {
	h.subscriptionQuota = checker
}

// tenantSubscriptionCount sums active subscriptions over the connected
// clients of one tenant. It is O(clients); subscription frames are rare
// relative to data frames, so no incremental counters are maintained.
func (h *Hub) tenantSubscriptionCount(tenant string) int {
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()

	total := 0
	for _, client := range clients {
		if client.tenant == tenant {
			total += client.subscriptionCount()
		}
	}
	return total
}

// add registers a connected client.
func (h *Hub) add(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

// ClientCount returns the number of currently connected WebSocket clients.
// It backs the nereidlink_ws_clients gauge, read at each Prometheus scrape.
func (h *Hub) ClientCount() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.clients))
}

// remove unregisters a client.
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// ListenAndServe runs the HTTP server until ctx is canceled.
func ListenAndServe(ctx context.Context, addr, wsPath string, shutdown time.Duration, handler http.Handler) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: shutdown,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdown)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return fmt.Errorf("gateway-api serve: %w", err)
	}
}
