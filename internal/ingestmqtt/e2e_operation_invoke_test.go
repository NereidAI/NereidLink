// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// e2e_operation_invoke_test.go drives the operation invoke packet over a
// real loopback TCP MQTT socket: the Core NATS dispatch frame reaches the
// gateway on v1/gateway/operations/invoke with params intact, and a result
// uplink on v1/gateway/operations/result is accepted for correlation
// (ADR-0035).

package ingestmqtt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/opinvoke"
)

// opSecretResolver returns fixed last-mile secret values.
type opSecretResolver struct {
	values map[string]string
}

func (o *opSecretResolver) ResolveConnectorSecrets(_ context.Context, _ string, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, name := range names {
		out[name] = o.values[name]
	}
	return out, nil
}

// TestE2EOperationInvokePacket proves the NL -> Edge invoke frame over the
// wire with its params. The reply subject is empty (no Core NATS in this
// harness), so correlation is intentionally not exercised.
func TestE2EOperationInvokePacket(t *testing.T) {
	e := startE2E(t)

	invokeCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayOperationsInvoke, 1, func(_ paho.Client, m paho.Message) {
		invokeCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe operations invoke: %v", token.Error())
	}

	relay := NewOperationRelay(nil, e.broker, e.hooks)

	frame := opinvoke.InvokeFrame{
		ID:        "task-1",
		TenantID:  "t",
		GatewayID: "gw",
		Operation: "ping",
		Params:    map[string]any{"host": "127.0.0.1"},
		TimeoutMs: 5000,
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	relay.handleDispatch(&nats.Msg{Data: raw})

	select {
	case m := <-invokeCh:
		if m.Topic() != mqttapi.TopicGatewayOperationsInvoke {
			t.Fatalf("unexpected topic %q", m.Topic())
		}
		var got gatewayOperationInvokeFrame
		if err := json.Unmarshal(m.Payload(), &got); err != nil {
			t.Fatalf("decode invoke frame: %v", err)
		}
		if got.ID != "task-1" || got.Operation != "ping" ||
			got.TimeoutMs != 5000 || got.Params["host"] != "127.0.0.1" {
			t.Fatalf("unexpected invoke frame: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/operations/invoke")
	}

	// Result uplink for an unknown (uncorrelated in this harness) task is
	// dropped without side effects; the frame still must parse and route.
	pub := e.client.Publish(mqttapi.TopicGatewayOperationsResult, 1, false,
		`{"id":"task-1","ok":true,"result":{"rtt_ms":12}}`)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish result: %v", pub.Error())
	}
	time.Sleep(100 * time.Millisecond)
}

// TestE2EOperationInvokeOfflineRejects ensures a dispatch frame naming a
// gateway with no live session produces no packet (reply path with empty
// subject is a silent no-op here).
func TestE2EOperationInvokeOfflineRejects(t *testing.T) {
	e := startE2E(t)

	invokeCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayOperationsInvoke, 1, func(_ paho.Client, m paho.Message) {
		invokeCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe operations invoke: %v", token.Error())
	}

	relay := NewOperationRelay(nil, e.broker, e.hooks)

	raw, err := json.Marshal(opinvoke.InvokeFrame{
		ID: "task-2", TenantID: "t", GatewayID: "other-gw",
		Operation: "ping", Params: map[string]any{}, TimeoutMs: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	relay.handleDispatch(&nats.Msg{Data: raw})

	select {
	case <-invokeCh:
		t.Fatal("frame for an offline gateway must not be written")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestE2EOperationInvokeSecretResolved verifies secret:// references in
// operation params are opened to plaintext at the last mile before the
// invoke packet reaches the gateway.
func TestE2EOperationInvokeSecretResolved(t *testing.T) {
	e := startE2E(t)

	invokeCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayOperationsInvoke, 1, func(_ paho.Client, m paho.Message) {
		invokeCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe operations invoke: %v", token.Error())
	}

	relay := NewOperationRelay(nil, e.broker, e.hooks)
	relay.SetSecretResolver(&opSecretResolver{values: map[string]string{
		"opcode": "4242",
	}})

	frame := opinvoke.InvokeFrame{
		ID:        "task-secret",
		TenantID:  "t",
		GatewayID: "gw",
		Operation: "maintenance",
		Params:    map[string]any{"code": "secret://opcode", "host": "127.0.0.1"},
		TimeoutMs: 5000,
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	relay.handleDispatch(&nats.Msg{Data: raw})

	select {
	case m := <-invokeCh:
		var got gatewayOperationInvokeFrame
		if err := json.Unmarshal(m.Payload(), &got); err != nil {
			t.Fatalf("decode invoke frame: %v", err)
		}
		if got.Params["code"] != "4242" {
			t.Fatalf("secret reference not resolved at last mile: %v", got.Params)
		}
		if got.Params["host"] != "127.0.0.1" {
			t.Fatalf("plaintext param altered: %v", got.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/operations/invoke")
	}
}
