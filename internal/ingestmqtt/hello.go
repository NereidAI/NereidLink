// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// hello.go implements the MQTT side of the NereidEdge hello exchange
// (ADR-0026): arming a per-connection negotiation timeout, translating a
// published hello through the transport-independent edge.Negotiator,
// returning the ack and enforcing the one-hello-per-connection rule.

package ingestmqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/nereidai/nereidlink/internal/connectorcatalog"
	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/secretcatalog"
)

// EdgeOperationRegistrar consumes the gateway operation descriptors
// advertised in hello and returns per-entry results. Implemented by
// *opcatalog.Service; nil disables registration.
type EdgeOperationRegistrar interface {
	RegisterFromHello(ctx context.Context, gatewayID string,
		regs []opcatalog.Descriptor) []opcatalog.Result
}

// EdgeSecretTypeRegistrar consumes the edge secret type descriptors
// advertised in hello and returns per-entry accept/reject results.
// Implemented by *secretcatalog.Service; nil disables registration.
type EdgeSecretTypeRegistrar interface {
	RegisterFromHello(ctx context.Context, tenantID, gatewayID string,
		regs []secretcatalog.Registration) []secretcatalog.TypeResult
}

// EdgeConnectorSchemaRegistrar consumes the inline connector config schemas
// advertised in hello and returns per-entry accept/reject results.
// Implemented by *connectorcatalog.Service; nil disables registration.
type EdgeConnectorSchemaRegistrar interface {
	RegisterFromHello(ctx context.Context, tenantID, gatewayID string,
		regs []connectorcatalog.Registration) []connectorcatalog.SchemaResult
}

// EdgeNegotiator is the subset of edge.Negotiator used by the publish
// hook. Declaring it here keeps mochi-free edge logic behind a narrow
// seam and makes the handler trivial to fake in tests.
type EdgeNegotiator interface {
	// Negotiate runs one hello exchange for a tenant.
	Negotiate(tenantID string, hello edge.Hello) (edge.Ack, edge.Session, error)

	// RemoveSession drops a session when its connection closes.
	RemoveSession(id string)
}

// helloEnforcer arms the 10s negotiation timeout for every gateway
// connection. A connection that does not complete hello in time is
// disconnected; completing hello cancels its timer.
type helloEnforcer struct {
	broker      *mqtt.Server
	connections *AuthHook
	timeout     time.Duration

	mu      sync.Mutex
	pending map[string]*time.Timer
}

// newHelloEnforcer constructs the enforcer.
func newHelloEnforcer(broker *mqtt.Server, connections *AuthHook, timeout time.Duration) *helloEnforcer {
	return &helloEnforcer{
		broker:      broker,
		connections: connections,
		timeout:     timeout,
		pending:     map[string]*time.Timer{},
	}
}

// arm starts the negotiation timeout for one gateway connection. It is
// called from the gateway-connect callback after authentication.
func (e *helloEnforcer) arm(clientID string) {
	timer := time.AfterFunc(e.timeout, func() {
		e.drop(clientID)
	})
	e.mu.Lock()
	if old, ok := e.pending[clientID]; ok {
		old.Stop()
	}
	e.pending[clientID] = timer
	e.mu.Unlock()
}

// cancel stops the pending timer after a successful hello.
func (e *helloEnforcer) cancel(clientID string) {
	e.mu.Lock()
	if timer, ok := e.pending[clientID]; ok {
		timer.Stop()
		delete(e.pending, clientID)
	}
	e.mu.Unlock()
}

// drop disconnects a still-unnnegotiated connection when the timeout
// fires. A connection that negotiated, disconnected, or vanished between
// scheduling and firing is left untouched.
func (e *helloEnforcer) drop(clientID string) {
	e.mu.Lock()
	delete(e.pending, clientID)
	e.mu.Unlock()

	if e.connections.isNegotiated(clientID) {
		return
	}
	client, ok := e.broker.Clients.Get(clientID)
	if !ok {
		return
	}
	slog.Warn("gateway hello timeout; disconnecting", "client", clientID)
	if err := e.broker.DisconnectClient(client, packets.CodeDisconnect); err != nil {
		slog.Warn("hello timeout disconnect failed", "client", clientID, "error", err)
	}
}

// handleHello negotiates one hello frame. Non-gateway identities, a
// second hello on an already-negotiated connection and malformed frames
// are all rejected. On success the ack is published, the connection is
// marked negotiated and the timeout is cancelled.
func (h *PublishHook) handleHello(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated hello reached publish hook", "client", clientID)
		return
	}
	if !isGatewayIdentity(current.identity) {
		h.Log.Warn("non-gateway device published hello", "client", clientID)
		return
	}
	if current.negotiated {
		// One negotiation per connection, per ADR.
		h.Log.Warn("duplicate hello rejected", "client", clientID)
		return
	}
	if h.edge == nil {
		h.Log.Error("edge negotiator not wired")
		return
	}

	var hello edge.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		h.Log.Warn("invalid hello payload", "client", clientID, "error", err)
		return
	}

	ack, session, err := h.edge.Negotiate(current.identity.TenantID, hello)
	if err != nil {
		h.Log.Warn("hello negotiation rejected frame", "client", clientID, "error", err)
		return
	}
	if ack.Status != edge.StatusOK {
		// Send the failure ack so the edge can see why, then leave the
		// connection un-negotiated; the timeout will close it.
		h.sendAck(clientID, hello.ResponseTopic, ack)
		h.Log.Warn("hello negotiation failed",
			"client", clientID, "status", ack.Status, "reason", ack.Reason)
		return
	}

	h.connections.markNegotiated(clientID, session.ID)
	// Echo the platform-resolved gateway device UUID so the edge can fill
	// LinkEpisodeReport.EdgeID, which it cannot derive from its credential.
	ack.GatewayID = current.identity.DeviceID
	h.connections.markFeatures(clientID, ack.Features)
	h.helloEnforcer.cancel(clientID)
	// Persist the negotiated feature set so the console can distinguish an
	// edge supporting secret delivery from unsupported firmware (ADR-0034).
	// Best-effort: a failure only logs; it never fails the session.
	if h.gateways != nil {
		if err := h.gateways.StampGatewayFeatures(context.Background(),
			current.identity.TenantID, current.identity.DeviceID,
			ack.Features); err != nil {
			h.Log.Warn("stamp gateway features failed", "error", err)
		}
	}
	if h.linkHealth != nil {
		h.linkHealth.ObserveConnect(context.Background(),
			current.identity.TenantID, current.identity.DeviceID, time.Now().UTC())
	}
	// Register the edge-advertised connector config schemas before the
	// post-hello redrive. Registration is best-effort and partial-success;
	// a malformed schema is reported in the ack but never fails the session.
	if h.connectorCatalog != nil && len(hello.Connectors) > 0 {
		regs := make([]connectorcatalog.Registration, 0, len(hello.Connectors))
		for _, c := range hello.Connectors {
			regs = append(regs, connectorcatalog.Registration{
				ConnectorType:        c.Type,
				ConnectorTypeVersion: c.TypeVersion,
				ConfigSchema:         c.ConfigSchema,
				Protocol:             c.Protocol,
				Transports:           c.Transports,
			})
		}
		results := h.connectorCatalog.RegisterFromHello(context.Background(),
			current.identity.TenantID, current.identity.DeviceID, regs)
		ack.ConnectorSchemas = make([]edge.ConnectorSchemaResult, 0, len(results))
		for _, r := range results {
			ack.ConnectorSchemas = append(ack.ConnectorSchemas, edge.ConnectorSchemaResult{
				Type:        r.ConnectorType,
				TypeVersion: r.ConnectorTypeVersion,
				Accepted:    r.Accepted,
				Reason:      r.Reason,
			})
		}
	}
	// Register the edge-advertised secret type descriptors (ADR-0034).
	// Same best-effort, partial-success contract.
	if h.secretCatalog != nil && len(hello.SecretTypes) > 0 {
		regs := make([]secretcatalog.Registration, 0, len(hello.SecretTypes))
		for _, a := range hello.SecretTypes {
			regs = append(regs, secretcatalog.Registration{
				SecretType:   a.Type,
				TypeVersion:  a.TypeVersion,
				FieldsSchema: a.FieldsSchema,
				DisplayName:  a.DisplayName,
			})
		}
		results := h.secretCatalog.RegisterFromHello(context.Background(),
			current.identity.TenantID, current.identity.DeviceID, regs)
		ack.SecretTypes = make([]edge.SecretTypeResult, 0, len(results))
		for _, r := range results {
			ack.SecretTypes = append(ack.SecretTypes, edge.SecretTypeResult{
				Type:        r.SecretType,
				TypeVersion: r.TypeVersion,
				Accepted:    r.Accepted,
				Reason:      r.Reason,
			})
		}
	}
	// Register the edge-advertised gateway operation descriptors
	// (ADR-0035). operations[] is the firmware's FULL current set and is
	// reconciled per gateway: an explicitly present (possibly empty) array
	// replaces the gateway's prior advertisements; a nil field means old
	// firmware that does not speak operations and is left untouched.
	if h.operationCatalog != nil && hello.Operations != nil {
		regs := make([]opcatalog.Descriptor, 0, len(hello.Operations))
		for _, a := range hello.Operations {
			regs = append(regs, opcatalog.Descriptor{
				Operation:    a.Operation,
				TypeVersion:  a.TypeVersion,
				Interaction:  a.Interaction,
				TimeoutMs:    a.TimeoutMs,
				Risk:         a.Risk,
				ParamSchema:  a.ParamSchema,
				ResultSchema: a.ResultSchema,
				DisplayName:  a.DisplayName,
				Description:  a.Description,
			})
		}
		results := h.operationCatalog.RegisterFromHello(context.Background(),
			current.identity.DeviceID, regs)
		ack.Operations = make([]edge.OperationResult, 0, len(results))
		advertised := make([]string, 0, len(results))
		for _, r := range results {
			ack.Operations = append(ack.Operations, edge.OperationResult{
				Operation:   r.Operation,
				TypeVersion: r.TypeVersion,
				Accepted:    r.Accepted,
				Reason:      r.Reason,
			})
			// Only descriptors accepted into the catalog describe actions
			// this gateway can currently execute.
			if r.Accepted {
				advertised = append(advertised, r.Operation)
			}
		}
		// Persist the advertised operation set so the gateway Operations tab
		// can be scoped to this firmware (ADR-0035). Best-effort.
		if h.gateways != nil {
			if err := h.gateways.StampGatewayOperations(context.Background(),
				current.identity.TenantID, current.identity.DeviceID, advertised); err != nil {
				h.Log.Warn("stamp gateway operations failed", "error", err)
			}
		}
	}
	// Re-send the full desired connector set now that the edge is
	// negotiated and subscribed to the downlink filter (ADR-0026: desired
	// never disappears; reconnect retries delivery idempotently).
	if h.connectorRedrive != nil {
		h.connectorRedrive.Redrive(context.Background(),
			current.identity.TenantID, current.identity.DeviceID)
	}
	// Re-send the full desired secret set when the session negotiated the
	// "secrets" feature (ADR-0034). Like connectors, the push is full-state
	// and idempotent; gating on the negotiated intersection keeps old
	// firmware from receiving frames it cannot parse.
	if h.secretRedrive != nil && slices.Contains(ack.Features, edge.FeatureSecrets) {
		if err := h.secretRedrive.Redrive(context.Background(),
			current.identity.TenantID, current.identity.DeviceID); err != nil {
			h.Log.Warn("secret redrive failed",
				"client", clientID, "gateway", current.identity.DeviceID, "error", err)
		}
	}
	// Device-profile downlink (ADR-0038): stamp the formats the platform
	// enabled for this session so the delivery path renders the right
	// document, then re-send the full desired profile set. The push is
	// full-state and idempotent, gated on the negotiated "profiles"
	// feature so old firmware never receives a frame it cannot parse.
	if slices.Contains(ack.Features, edge.FeatureProfiles) {
		if h.gateways != nil {
			if err := h.gateways.StampGatewayProfileFormats(context.Background(),
				current.identity.TenantID, current.identity.DeviceID, ack.ProfileFormats); err != nil {
				h.Log.Warn("stamp gateway profile formats failed", "error", err)
			}
		}
		if h.profileRedrive != nil {
			if err := h.profileRedrive.Redrive(context.Background(),
				current.identity.TenantID, current.identity.DeviceID); err != nil {
				h.Log.Warn("profile redrive failed",
					"client", clientID, "gateway", current.identity.DeviceID, "error", err)
			}
		}
	}
	// Edge model inference (ADR-0039): persist the accepted contract and
	// backend intersection. A nil block stamps an empty capability so the
	// runtime stale watchdog only considers inference-capable gateways.
	// Best-effort, like the other capability stamps; never fails the session.
	if h.gateways != nil {
		var contract string
		var backends []string
		if ack.Inference != nil {
			contract = ack.Inference.Contract
			backends = ack.Inference.Backends
		}
		if err := h.gateways.StampGatewayInference(context.Background(),
			current.identity.TenantID, current.identity.DeviceID,
			contract, backends); err != nil {
			h.Log.Warn("stamp gateway inference failed", "error", err)
		}
	}
	h.sendAck(clientID, hello.ResponseTopic, ack)
}

// sendAck publishes the negotiation response to the edge, using the
// edge-provided response topic or the built-in default.
func (h *PublishHook) sendAck(clientID, responseTopic string, ack edge.Ack) {
	topic := responseTopic
	if topic == "" {
		topic = mqttapi.TopicGatewayHelloAck
	}
	client, ok := h.brokerClient(clientID)
	if !ok {
		return
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		h.Log.Warn("hello ack encode failed", "client", clientID, "error", err)
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
		h.Log.Warn("hello ack write failed", "client", clientID, "error", err)
	}
}

// brokerClient resolves the live mochi client, kept as a small indirection
// so tests without a broker short-circuit cleanly.
func (h *PublishHook) brokerClient(clientID string) (*mqtt.Client, bool) {
	if h.broker == nil {
		return nil, false
	}
	return h.broker.Clients.Get(clientID)
}
