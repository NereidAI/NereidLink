// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// operation_relay.go bridges synchronous gateway operation requests with
// the owning gateway session (ADR-0035): a Core NATS request on
// nl.edge.op.dispatch.<ingress> becomes one MQTT invoke packet on
// v1/gateway/operations/invoke, and the edge result on
// v1/gateway/operations/result is published back onto the request reply
// subject. Params carry secret:// references that are resolved here at the
// last mile.

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

	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/opinvoke"
	"github.com/nereidai/nereidlink/internal/secrets"
)

// gatewayOperationInvokeFrame is the JSON packet delivered on
// v1/gateway/operations/invoke.
type gatewayOperationInvokeFrame struct {
	// ID is the task id correlated with the result.
	ID string `json:"id"`
	// Operation is the catalog key.
	Operation string `json:"operation"`
	// TypeVersion is the descriptor version.
	TypeVersion string `json:"type_version,omitempty"`
	// Params is the resolved parameter document.
	Params map[string]any `json:"params"`
	// TimeoutMs is the edge-side execution budget.
	TimeoutMs int `json:"timeout_ms"`
}

// gatewayOperationResultFrame is the edge uplink on
// v1/gateway/operations/result.
type gatewayOperationResultFrame struct {
	// ID is the task id.
	ID string `json:"id"`
	// OK reports edge-level success.
	OK bool `json:"ok"`
	// Result is any JSON document.
	Result json.RawMessage `json:"result,omitempty"`
	// Error carries a failure explanation.
	Error string `json:"error,omitempty"`
}

// pendingOp correlates one request with its NATS reply subject.
type pendingOp struct {
	// reply is the Core NATS subject awaiting the result.
	reply string
	// stop cancels the cleanup timer when the result arrives first.
	stop func()
}

// OperationRelay routes operation invokes and results.
type OperationRelay struct {
	nc      *nats.Conn
	broker  *mqtt.Server
	hooks   *AuthHook
	secrets ConnectorSecretResolver

	mu      sync.Mutex
	pending map[string]pendingOp
}

// NewOperationRelay constructs an operation relay.
func NewOperationRelay(nc *nats.Conn, broker *mqtt.Server, hooks *AuthHook) *OperationRelay {
	return &OperationRelay{
		nc:      nc,
		broker:  broker,
		hooks:   hooks,
		pending: map[string]pendingOp{},
	}
}

// SetSecretResolver wires last-mile secret:// resolution into invoke
// params. Typically the same store as the connector relay is used.
func (r *OperationRelay) SetSecretResolver(resolver ConnectorSecretResolver) {
	r.secrets = resolver
}

// Subscribe listens for dispatch requests for ingressID.
func (r *OperationRelay) Subscribe(ingressID string) error {
	_, err := r.nc.Subscribe(opinvoke.DispatchSubjectPrefix+ingressID, r.handleDispatch)
	return err
}

// replyError publishes one failure ResultFrame onto a request reply
// subject, so the requester never waits for its timeout on a known
// failure.
func (r *OperationRelay) replyError(reply, message string) {
	if reply == "" {
		return
	}
	raw, err := json.Marshal(opinvoke.ResultFrame{OK: false, Error: message})
	if err != nil {
		return
	}
	_ = r.nc.Publish(reply, raw)
}

// handleDispatch resolves the live session, opens secret references, writes
// the invoke packet, and arms correlation (or replies immediately for a
// one-way operation).
func (r *OperationRelay) handleDispatch(msg *nats.Msg) {
	var frame opinvoke.InvokeFrame
	if err := json.Unmarshal(msg.Data, &frame); err != nil {
		r.replyError(msg.Reply, "invalid invoke frame")
		return
	}
	clientID, ok := r.hooks.clientIDFor(frame.TenantID, frame.GatewayID)
	if !ok {
		r.replyError(msg.Reply, "gateway not connected")
		return
	}
	client, ok := r.broker.Clients.Get(clientID)
	if !ok {
		r.replyError(msg.Reply, "gateway session vanished")
		return
	}

	params, missing := r.resolveParams(frame)
	if len(missing) > 0 {
		slog.Warn("operation invoke references unresolved secrets",
			"operation", frame.Operation, "names", strings.Join(missing, ","))
	}

	packet, err := json.Marshal(gatewayOperationInvokeFrame{
		ID:          frame.ID,
		Operation:   frame.Operation,
		TypeVersion: frame.TypeVersion,
		Params:      params,
		TimeoutMs:   frame.TimeoutMs,
	})
	if err != nil {
		r.replyError(msg.Reply, "encode invoke packet failed")
		return
	}
	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   mqttapi.TopicGatewayOperationsInvoke,
		Payload:     packet,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		r.replyError(msg.Reply, "invoke write failed: "+err.Error())
		return
	}

	if frame.OneWay {
		// No edge result: packet written is the terminal success.
		r.replyResult(msg.Reply, opinvoke.ResultFrame{
			ID: frame.ID, OK: true,
			Result: json.RawMessage(`{"delivered":true}`),
		})
		return
	}
	r.armCorrelation(frame.ID, msg.Reply, frame.TimeoutMs)
}

// resolveParams opens the secret:// references in the invoke params at the
// last mile. Unresolved references are sent verbatim and listed.
func (r *OperationRelay) resolveParams(frame opinvoke.InvokeFrame) (map[string]any, []string) {
	params := frame.Params
	if params == nil {
		params = map[string]any{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return params, nil
	}
	names := secrets.FindReferences(raw)
	if len(names) == 0 || r.secrets == nil {
		return params, nil
	}
	values, err := r.secrets.ResolveConnectorSecrets(
		context.Background(), frame.TenantID, names)
	if err != nil {
		slog.Warn("operation invoke secret resolution failed; sending references",
			"operation", frame.Operation, "error", err)
		return params, nil
	}
	resolvedRaw, missing := secrets.ResolveDocument(raw, values)
	var resolved map[string]any
	if uerr := json.Unmarshal(resolvedRaw, &resolved); uerr != nil {
		return params, nil
	}
	return resolved, missing
}

// armCorrelation stores the reply subject for one request and removes it
// if the edge result never arrives, bounded by the descriptor timeout plus
// a grace period for the transport.
func (r *OperationRelay) armCorrelation(id, reply string, timeoutMs int) {
	wait := time.Duration(timeoutMs)*time.Millisecond + 2*time.Second
	if timeoutMs <= 0 {
		wait = time.Duration(opinvoke.MaxWaitMs)*time.Millisecond + 2*time.Second
	}
	timer := time.AfterFunc(wait, func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	})
	r.mu.Lock()
	if old, ok := r.pending[id]; ok {
		old.stop()
	}
	r.pending[id] = pendingOp{
		reply: reply,
		stop:  func() { timer.Stop() },
	}
	r.mu.Unlock()
}

// ReportResult correlates an edge-reported result uplink and publishes it
// onto the original request reply subject. A stale result (timed out
// requester) is dropped.
func (r *OperationRelay) ReportResult(payload []byte) {
	var frame gatewayOperationResultFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return
	}
	r.mu.Lock()
	pending, ok := r.pending[frame.ID]
	if ok {
		pending.stop()
		delete(r.pending, frame.ID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.replyResult(pending.reply, opinvoke.ResultFrame{
		ID:     frame.ID,
		OK:     frame.OK,
		Result: frame.Result,
		Error:  frame.Error,
	})
}

// replyResult publishes one result frame onto a Core NATS subject.
func (r *OperationRelay) replyResult(reply string, frame opinvoke.ResultFrame) {
	if reply == "" {
		return
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	_ = r.nc.Publish(reply, raw)
}
