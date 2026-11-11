// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/profilepush"
	"github.com/nereidai/nereidlink/internal/secretbinding"
	"github.com/nereidai/nereidlink/internal/secrets"
	"github.com/nereidai/nereidlink/internal/shadow"
)

// downlinkFrame is the JSON payload delivered on v1/me/cmd/<name>. Params is
// the catalog-validated JSON document delivered verbatim.
type downlinkFrame struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Params    json.RawMessage `json:"params"`
	Direction string          `json:"direction,omitempty"`
}

// gatewayCommandFrame is the JSON payload delivered on v1/gateway/command to
// one owning gateway for an attached sub-device (protocol v1.md). It carries
// the target sub-device explicitly because a gateway holds one subscription
// for every attached device, plus the interaction/timeliness mode names used
// by the edge protocol (method rather than name).
type gatewayCommandFrame struct {
	CommandID   string          `json:"command_id"`
	Device      string          `json:"device"`
	Interaction string          `json:"interaction"`
	Timeliness  string          `json:"timeliness"`
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params"`
	Direction   string          `json:"direction,omitempty"`
}

// gatewayCommandResponseFrame is the uplink a gateway publishes on
// v1/gateway/command/response for one sub-device command.
type gatewayCommandResponseFrame struct {
	CommandID string          `json:"command_id"`
	Device    string          `json:"device"`
	Data      json.RawMessage `json:"data"`
}

// pendingResponse correlates one request-response command with the NATS
// reply subject while waiting for the device response.
type pendingResponse struct {
	// reply is the Core NATS subject answering the dispatch request.
	reply string

	// stop cancels the timeout timer when the response arrives first.
	stop func()
}

// CommandRelay bridges Core NATS dispatch requests with transport sessions
// owned by this ingress node. Delivery is targeted at the one owning client
// instead of published through the shared command filter.
type CommandRelay struct {
	nc     *nats.Conn
	broker *mqtt.Server
	hooks  *AuthHook

	// secrets resolves secret:// references in connector config at the
	// downlink last mile (ADR-0026, migration 0047). nil leaves references
	// verbatim (feature disabled).
	secrets ConnectorSecretResolver

	// deliveryDocs resolves typed documents inside secret delivery frames
	// (ADR-0034). nil disables the last-mile substitution.
	deliveryDocs DeliverySecretResolver

	// dispatchMarker records DISPATCHED revisions after a successful write.
	dispatchMarker SecretDispatchMarker

	// profileMarker records DISPATCHED profile revisions after a successful
	// write (ADR-0038). nil disables dispatch marking.
	profileMarker ProfileDispatchMarker

	mu       sync.Mutex
	pending  map[string]pendingResponse
	tenantOf map[string]string
	deviceOf map[string]string
}

// ConnectorSecretResolver loads the named plaintext secrets for one tenant.
// *secrets.Store satisfies it.
type ConnectorSecretResolver interface {
	ResolveConnectorSecrets(ctx context.Context, tenantID string, names []string) (map[string]string, error)
}

// DeliverySecretResolver loads typed secret documents for last-mile
// resolution of secret delivery frames (ADR-0034). *secrets.Store
// satisfies it.
type DeliverySecretResolver interface {
	ResolveDocuments(ctx context.Context, tenantID string, names []string) (map[string]map[string]any, error)
}

// SecretDispatchMarker marks binding revisions as DISPATCHED after the
// MQTT packet is written. *secretbinding.Service satisfies it.
type SecretDispatchMarker interface {
	MarkDispatched(ctx context.Context, tenantID, gatewayID string, revisions []int64) error
}

// ProfileDispatchMarker marks one profile revision as DISPATCHED after the
// MQTT packet is written. *profilepush.Service satisfies it.
type ProfileDispatchMarker interface {
	MarkDispatched(ctx context.Context, tenantID, gatewayID, profileID string, revision int64) error
}

// NewCommandRelay constructs the ingress command relay.
func NewCommandRelay(nc *nats.Conn, broker *mqtt.Server, hooks *AuthHook) *CommandRelay {
	return &CommandRelay{
		nc:       nc,
		broker:   broker,
		hooks:    hooks,
		pending:  map[string]pendingResponse{},
		tenantOf: map[string]string{},
		deviceOf: map[string]string{},
	}
}

// SetSecretResolver wires last-mile secret:// resolution into connector
// downlink frames.
func (r *CommandRelay) SetSecretResolver(resolver ConnectorSecretResolver) {
	r.secrets = resolver
}

// SetDeliverySecretResolver wires last-mile plaintext substitution into
// secret delivery frames and the dispatch marker (ADR-0034).
func (r *CommandRelay) SetDeliverySecretResolver(resolver DeliverySecretResolver, marker SecretDispatchMarker) {
	r.deliveryDocs = resolver
	r.dispatchMarker = marker
}

// SetProfileDispatchMarker wires DISPATCHED marking for profile delivery
// frames (ADR-0038).
func (r *CommandRelay) SetProfileDispatchMarker(marker ProfileDispatchMarker) {
	r.profileMarker = marker
}

// Subscribe begins listening for dispatch requests for ingressID.
func (r *CommandRelay) Subscribe(ingressID string) error {
	if _, err := r.nc.Subscribe(command.DispatchSubjectPrefix+ingressID, r.handleDispatch); err != nil {
		return err
	}
	if err := r.SubscribeShadow(); err != nil {
		return err
	}
	if err := r.SubscribeAttributes(); err != nil {
		return err
	}
	if err := r.SubscribeConnectors(); err != nil {
		return err
	}
	if err := r.SubscribeSecrets(); err != nil {
		return err
	}
	return r.SubscribeProfiles()
}

// SubscribeAttributes receives server/shared attribute pushes for connected
// devices (ADR-0011). Delivery is best-effort: a device with no live session
// on this ingress gets no packet and reads current attributes on its next
// fetch/handshake instead.
func (r *CommandRelay) SubscribeAttributes() error {
	_, err := r.nc.Subscribe(mqttapi.SubjectAttributePushPrefix+".>",
		r.handleAttributePush)
	return err
}

// SubscribeShadow receives desired-state pushes for connected devices.
func (r *CommandRelay) SubscribeShadow() error {
	_, err := r.nc.Subscribe(shadow.SubjectDesiredPrefix+".>", r.handleShadowDesired)
	return err
}

// SubscribeConnectors receives desired connector spec/delete pushes for
// connected gateways (ADR-0026). Delivery is best-effort and idempotent: an
// offline gateway simply gets the full desired set again on its next hello.
func (r *CommandRelay) SubscribeConnectors() error {
	_, err := r.nc.Subscribe(connector.SubjectDesiredPrefix+".>", r.handleConnectorDesired)
	return err
}

// SubscribeSecrets receives desired secret delivery frames for connected
// gateways (ADR-0034). Frames are full-state and idempotent; an offline
// gateway gets the whole set again on its next hello.
func (r *CommandRelay) SubscribeSecrets() error {
	_, err := r.nc.Subscribe(secretbinding.SubjectDesiredPrefix+".>", r.handleSecretDesired)
	return err
}

// SubscribeProfiles receives desired device-profile frames for connected
// gateways (ADR-0038). Frames carry the full desired set and are idempotent:
// an offline gateway simply gets the whole set again on its next hello.
func (r *CommandRelay) SubscribeProfiles() error {
	_, err := r.nc.Subscribe(profilepush.SubjectDesiredPrefix+".>", r.handleProfileDesired)
	return err
}

// handleProfileDesired writes the full desired profile set to the owning
// gateway on v1/gateway/profiles/spec, gated on the negotiated profiles
// feature (ADR-0038). Documents are already rendered cloud-side, so the relay
// only forwards the frame; marking PENDING rows DISPATCHED happens after the
// packet is written. No live session means the push is skipped and the
// post-hello redrive covers reconnects.
func (r *CommandRelay) handleProfileDesired(msg *nats.Msg) {
	var frame profilepush.PushFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		return
	}
	// Feature gate (ADR-0038): a profile may be targeted at firmware that
	// does not implement profile downlink; such a session must not be sent
	// the frame. The row stays PENDING and is delivered once a supporting
	// firmware negotiates the feature.
	if !r.hooks.clientHasFeature(clientID, edge.FeatureProfiles) {
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayProfilesSpec,
		Payload:     msg.Data,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("profile desired write failed", "error", err)
		return
	}
	if r.profileMarker != nil {
		for _, entry := range frame.Entries {
			if entry.Removing {
				continue
			}
			if err := r.profileMarker.MarkDispatched(context.Background(),
				frame.TenantID, frame.GatewayID, entry.ProfileID, entry.Revision); err != nil {
				slog.Warn("profile dispatch marking failed", "error", err)
			}
		}
	}
}

// handleSecretDesired resolves the field references in the frame solely
// into the one MQTT packet written to the owning gateway, then marks the
// revisions DISPATCHED. No live session means the push is skipped; the
// post-hello redrive covers reconnects.
func (r *CommandRelay) handleSecretDesired(msg *nats.Msg) {
	if r.deliveryDocs == nil {
		return
	}
	var frame secretbinding.PushFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		return
	}
	// Feature gate (ADR-0034): the binding may be recorded for firmware
	// that does not implement secret delivery; such a session must not be
	// sent the sync frame. The row stays PENDING and is delivered once a
	// supporting firmware negotiates the feature.
	if !r.hooks.clientHasFeature(clientID, edge.FeatureSecrets) {
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	names := map[string]struct{}{}
	for _, entry := range frame.Entries {
		collectFieldRefs(entry.Document, names)
	}
	resolved := map[string]map[string]any{}
	if len(names) > 0 {
		list := make([]string, 0, len(names))
		for name := range names {
			list = append(list, name)
		}
		documents, err := r.deliveryDocs.ResolveDocuments(context.Background(), frame.TenantID, list)
		if err != nil {
			slog.Warn("secret delivery resolution failed; sending references",
				"gateway", frame.GatewayID, "error", err)
			return
		}
		resolved = documents
	}
	payload := msg.Data
	missing := []string{}
	if len(names) > 0 {
		for i := range frame.Entries {
			missing = append(missing, secretbinding.ResolveEntryFields(frame.Entries[i].Document, resolved)...)
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			return
		}
		payload = raw
	}
	if len(missing) > 0 {
		slog.Warn("secret delivery frame references unresolved secrets",
			"gateway", frame.GatewayID, "names", strings.Join(missing, ","))
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewaySecretsSync,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("secret desired write failed", "error", err)
		return
	}
	if r.dispatchMarker != nil {
		revisions := make([]int64, 0, len(frame.Entries))
		for _, entry := range frame.Entries {
			revisions = append(revisions, entry.Revision)
		}
		if err := r.dispatchMarker.MarkDispatched(context.Background(),
			frame.TenantID, frame.GatewayID, revisions); err != nil {
			slog.Warn("secret dispatch marking failed", "error", err)
		}
	}
}

// collectFieldRefs gathers the distinct secret names referenced in a
// delivery document.
func collectFieldRefs(doc map[string]any, names map[string]struct{}) {
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		case string:
			if name, _, ok := secretbinding.ParseFieldReference(node); ok {
				names[name] = struct{}{}
			}
		}
	}
	walk(doc)
}

func (r *CommandRelay) handleConnectorDesired(msg *nats.Msg) {
	var frame connector.PushFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	topic := mqttapi.TopicGatewayConnectorSpec
	if frame.Kind == connector.KindDelete {
		topic = mqttapi.TopicGatewayConnectorDelete
	}
	payload := msg.Data
	// Last-mile secret:// resolution (migration 0047): the persisted spec and
	// the Core NATS frame carry only references; plaintext is substituted
	// here, solely into the one MQTT packet written to the owning gateway.
	if frame.Kind == connector.KindSpec && r.secrets != nil && len(frame.Config) > 0 {
		if resolved, rerr := r.resolveConnectorSecrets(frame); rerr != nil {
			slog.Warn("connector secret resolution failed; sending references",
				"spec", frame.SpecID, "error", rerr)
		} else if resolved != nil {
			payload = resolved
		}
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   topic,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("connector desired write failed", "error", err, "kind", frame.Kind)
	}
}

// resolveConnectorSecrets substitutes every secret:// reference in the frame
// config and returns the re-marshaled frame bytes. It returns nil bytes when
// the frame carries no references.
func (r *CommandRelay) resolveConnectorSecrets(frame connector.PushFrame) ([]byte, error) {
	names := secrets.FindReferences(frame.Config)
	if len(names) == 0 {
		return nil, nil
	}
	values, err := r.secrets.ResolveConnectorSecrets(context.Background(), frame.TenantID, names)
	if err != nil {
		return nil, err
	}
	resolved, missing := secrets.ResolveDocument(frame.Config, values)
	if len(missing) > 0 {
		slog.Warn("connector frame references unresolved secrets",
			"spec", frame.SpecID, "names", strings.Join(missing, ","))
	}
	frame.Config = resolved
	return json.Marshal(frame)
}

// handleAttributePush delivers one server/shared attribute change to the live
// session: a directly connected device on v1/me/attributes/down, or, when the
// target is attached behind a gateway, to the owning gateway on
// v1/gateway/attributes/push naming the sub-device. No live session on this
// ingress means the frame is skipped (best-effort, idempotent).
func (r *CommandRelay) handleAttributePush(msg *nats.Msg) {
	var frame mqttapi.AttributePushFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		return
	}
	clientID, topic, payload, ok := r.resolveAttributePush(frame)
	if !ok {
		return
	}
	r.writeAttributePacket(clientID, topic, payload, frame.DeviceID)
}

// resolveAttributePush picks the live session and the MQTT wire frame for one
// server/shared attribute change: a direct device session wins, otherwise the
// gateway that attached it. ok is false when the frame is malformed or no live
// session on this ingress owns the device, in which case delivery is skipped.
// It is split from the packet write so routing can be unit tested without a
// broker.
func (r *CommandRelay) resolveAttributePush(frame mqttapi.AttributePushFrame) (clientID, topic string, payload []byte, ok bool) {
	if frame.TenantID == "" || frame.DeviceID == "" || frame.Key == "" {
		return "", "", nil, false
	}
	data := map[string]json.RawMessage{frame.Key: frame.Value}
	if directID, found := r.hooks.clientIDFor(frame.TenantID, frame.DeviceID); found {
		payload, err := json.Marshal(mqttapi.DeviceAttributesDownFrame{Scope: frame.Scope, Data: data})
		if err != nil {
			return "", "", nil, false
		}
		return directID, mqttapi.TopicDeviceAttributesDown, payload, true
	}
	if gatewayID, found := r.hooks.gatewayClientIDFor(frame.TenantID, frame.DeviceID); found {
		payload, err := json.Marshal(mqttapi.GatewayAttributesPushFrame{
			Device: frame.DeviceID, Scope: frame.Scope, Data: data,
		})
		if err != nil {
			return "", "", nil, false
		}
		return gatewayID, mqttapi.TopicGatewayAttributesPush, payload, true
	}
	return "", "", nil, false
}

// writeAttributePacket writes an already-marshaled frame as one MQTT PUBLISH
// to the given client. A vanished client or transport error is logged, not
// propagated.
func (r *CommandRelay) writeAttributePacket(clientID, topic string, payload []byte, deviceID string) {
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   topic,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		slog.Warn("attribute push write failed", "device", deviceID, "error", err)
	}
}

func (r *CommandRelay) handleShadowDesired(msg *nats.Msg) {
	var frame shadow.PushFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		return
	}
	clientID, topic, payload, ok := r.resolveShadowDesired(frame)
	if !ok {
		return
	}
	r.writeAttributePacket(clientID, topic, payload, frame.DeviceID)
}

// resolveShadowDesired picks the live session and MQTT wire frame for one
// desired-state push: a direct device session (including a gateway acting on
// its own behalf) wins, otherwise the gateway that attached the sub-device.
// Unlike attributes, both device-facing frames are transcoded from the
// internal NATS frame so no tenant/device internals leak onto the wire, and
// the gateway frame names the proxied sub-device. Split from the packet write
// so routing is unit-testable without a broker.
func (r *CommandRelay) resolveShadowDesired(frame shadow.PushFrame) (clientID, topic string, payload []byte, ok bool) {
	if frame.TenantID == "" || frame.DeviceID == "" {
		return "", "", nil, false
	}
	if directID, found := r.hooks.clientIDFor(frame.TenantID, frame.DeviceID); found {
		out, err := json.Marshal(mqttapi.DeviceShadowDesiredFrame{
			Version: frame.Version,
			Desired: frame.State,
		})
		if err != nil {
			return "", "", nil, false
		}
		return directID, mqttapi.TopicDeviceShadowDesired, out, true
	}
	if gatewayID, found := r.hooks.gatewayClientIDFor(frame.TenantID, frame.DeviceID); found {
		out, err := json.Marshal(mqttapi.GatewayShadowDesiredFrame{
			Desired: []mqttapi.GatewayShadowDesired{{
				Device:  frame.DeviceID,
				Version: frame.Version,
				Desired: frame.State,
			}},
		})
		if err != nil {
			return "", "", nil, false
		}
		return gatewayID, mqttapi.TopicGatewayShadowDesired, out, true
	}
	return "", "", nil, false
}

// handleDispatch delivers one command directly to the owning client and
// correlates request-response commands with later uplink responses. A command
// carrying a gateway id targets a sub-device and is delivered on the gateway
// topic instead of the direct-device command topic.
func (r *CommandRelay) handleDispatch(msg *nats.Msg) {
	var wire v1.Command
	if err := proto.Unmarshal(msg.Data, &wire); err != nil {
		r.reply(msg.Reply, &v1.CommandAck{
			Status: command.StatusFailed,
			Error:  "invalid command",
		})
		return
	}

	if wire.GatewayId != "" {
		r.handleGatewayDispatch(&wire, msg.Reply)
		return
	}

	clientID, ok := r.hooks.clientIDFor(wire.TenantId, wire.DeviceId)
	if !ok {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusNotConnected,
			Error:  "device not connected",
		})
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusNotConnected,
			Error:  "device not connected",
		})
		return
	}

	payload, err := json.Marshal(downlinkFrame{
		ID:        wire.Id,
		Name:      wire.Name,
		Params:    wire.Params,
		Direction: wire.Direction,
	})
	if err != nil {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusFailed,
			Error:  "frame encode failed",
		})
		return
	}

	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.PrefixDeviceCommands + wire.Name,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion

	if err := client.WritePacket(out); err != nil {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusFailed,
			Error:  "transport write failed",
		})
		return
	}

	if wire.Interaction != command.InteractionRequestResponse {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusDelivered,
		})
		return
	}

	r.awaitResponse(&wire, msg.Reply)
}

// handleGatewayDispatch delivers one command to a sub-device through its live
// gateway. The target gateway must be connected on this ingress and the
// sub-device must be attached to that same connection (the in-memory
// attachment set the connect/manifest paths populate after registry
// authorization), which keeps one gateway from commanding another gateway's
// sub-device without a per-command database lookup. Persistent (offline)
// commands are not relayed here: presence reported a live gateway, and a
// missing attachment terminalizes as not-connected.
func (r *CommandRelay) handleGatewayDispatch(wire *v1.Command, reply string) {
	clientID, payload, ack := r.resolveGatewayCommand(wire)
	if ack != nil {
		r.reply(reply, ack)
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		r.reply(reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusNotConnected,
			Error:  "gateway not connected",
		})
		return
	}

	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayCommand,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		r.reply(reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusFailed,
			Error:  "transport write failed",
		})
		return
	}

	if wire.Interaction != command.InteractionRequestResponse {
		r.reply(reply, &v1.CommandAck{Id: wire.Id, Status: command.StatusDelivered})
		return
	}
	r.awaitResponse(wire, reply)
}

// resolveGatewayCommand decides the gateway client and marshaled frame for one
// sub-device command, or a terminal ack when the gateway/sub-device is not
// reachable on this ingress. It is split from packet writing so routing and
// wire encoding can be unit tested without a broker.
func (r *CommandRelay) resolveGatewayCommand(wire *v1.Command) (clientID string, payload []byte, ack *v1.CommandAck) {
	notConnected := &v1.CommandAck{
		Id:     wire.Id,
		Status: command.StatusNotConnected,
		Error:  "gateway or sub-device not connected",
	}
	clientID, ok := r.hooks.clientIDFor(wire.TenantId, wire.GatewayId)
	if !ok || !r.hooks.isSubdevice(clientID, wire.TenantId, wire.DeviceId) {
		return "", nil, notConnected
	}
	payload, err := json.Marshal(gatewayCommandFrame{
		CommandID:   wire.Id,
		Device:      wire.DeviceId,
		Interaction: wire.Interaction,
		Timeliness:  wire.Timeliness,
		Method:      wire.Name,
		Params:      wire.Params,
		Direction:   wire.Direction,
	})
	if err != nil {
		return "", nil, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusFailed,
			Error:  "frame encode failed",
		}
	}
	return clientID, payload, nil
}

// awaitResponse registers the correlation and replies TIMEOUT when the device
// response does not arrive within the command timeout.
func (r *CommandRelay) awaitResponse(wire *v1.Command, reply string) {
	timeout := time.Duration(wire.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	timer := time.AfterFunc(timeout, func() {
		r.mu.Lock()
		_, awaited := r.pending[wire.Id]
		delete(r.pending, wire.Id)
		delete(r.tenantOf, wire.Id)
		delete(r.deviceOf, wire.Id)
		r.mu.Unlock()
		if !awaited {
			return
		}
		r.reply(reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusTimeout,
			Error:  "response timeout",
		})
	})

	r.mu.Lock()
	r.pending[wire.Id] = pendingResponse{
		reply: reply,
		stop:  func() { timer.Stop() },
	}
	r.tenantOf[wire.Id] = wire.TenantId
	r.deviceOf[wire.Id] = wire.DeviceId
	r.mu.Unlock()
}

// ReportResponse correlates a device response uplink with its command. When
// the synchronous wait still exists the dispatch request is answered;
// otherwise the late result is published as a lifecycle event, per ADR-0012.
func (r *CommandRelay) ReportResponse(tenantID, deviceID, commandID string, payload []byte) {
	r.mu.Lock()
	pending, ok := r.pending[commandID]
	if ok {
		pending.stop()
		delete(r.pending, commandID)
		delete(r.tenantOf, commandID)
		delete(r.deviceOf, commandID)
	}
	r.mu.Unlock()

	if ok {
		r.reply(pending.reply, &v1.CommandAck{
			Id:       commandID,
			Status:   command.StatusSucceeded,
			Response: payload,
		})
		return
	}

	event, err := json.Marshal(command.Event{
		CommandID: commandID,
		TenantID:  tenantID,
		DeviceID:  deviceID,
		Status:    command.StatusSucceeded,
		Response:  string(payload),
		Time:      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = r.nc.Publish(command.EventsSubjectPrefix+tenantID+"."+deviceID, event)
}

// reply publishes one ack onto the Core NATS reply subject.
func (r *CommandRelay) reply(subject string, ack *v1.CommandAck) {
	if subject == "" {
		return
	}
	raw, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	_ = r.nc.Publish(subject, raw)
}

// responseCommandID extracts the command ID suffix from an uplink topic.
func responseCommandID(topic string) (string, bool) {
	if !strings.HasPrefix(topic, mqttapi.PrefixDeviceCommandResponse) {
		return "", false
	}
	id := topic[len(mqttapi.PrefixDeviceCommandResponse):]
	if id == "" {
		return "", false
	}
	return id, true
}
