// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// e2e_connector_delivery_test.go drives connector spec/delete downlinks and
// optional status projections through the real loopback MQTT broker hooks.

package ingestmqtt

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

type recordingConnectorStatusReceiver struct {
	mu      sync.Mutex
	tenants []string
	frames  []connector.StatusFrame
}

func (r *recordingConnectorStatusReceiver) HandleStatus(_ context.Context, tenantID string, frame connector.StatusFrame) (connector.Spec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants = append(r.tenants, tenantID)
	r.frames = append(r.frames, frame)
	return connector.Spec{ID: frame.SpecID, TenantID: tenantID, Revision: frame.Revision}, nil
}

func (r *recordingConnectorStatusReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func TestE2EConnectorStatusOptionalFieldsAndIngressGates(t *testing.T) {
	e := startE2E(t)
	receiver := &recordingConnectorStatusReceiver{}
	e.publishHook.SetConnectorStatusReceiver(receiver)

	for _, invalid := range []string{
		`{"spec_id":"s1","state":"APPLIED","revision":1,"stats":{"success_total":-1}}`,
		`{"spec_id":"s1","state":"APPLIED","revision":1,"stats":{"error_total":1.5}}`,
	} {
		token := e.client.Publish(mqttapi.TopicGatewayConnectorStatus, 1, false, invalid)
		if token.Wait() && token.Error() != nil {
			t.Fatalf("publish invalid counter: %v", token.Error())
		}
	}
	time.Sleep(150 * time.Millisecond)
	if receiver.count() != 0 {
		t.Fatal("negative or fractional counters reached the connector service")
	}

	valid := `{"spec_id":"s1","state":"APPLIED","revision":3,"tenant_id":"foreign","stats":{"success_total":0,"p50_ms":47.5},"observed_revision":3,"observed_hash":"abc123"}`
	token := e.client.Publish(mqttapi.TopicGatewayConnectorStatus, 1, false, valid)
	if token.Wait() && token.Error() != nil {
		t.Fatalf("publish valid status: %v", token.Error())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && receiver.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	receiver.mu.Lock()
	if len(receiver.frames) != 1 {
		receiver.mu.Unlock()
		t.Fatalf("valid status calls=%d, want 1", len(receiver.frames))
	}
	frame := receiver.frames[0]
	tenant := receiver.tenants[0]
	receiver.mu.Unlock()
	if tenant != "t" {
		t.Fatalf("tenant came from payload instead of authenticated session: %q", tenant)
	}
	if frame.Stats == nil || frame.Stats.SuccessTotal == nil || *frame.Stats.SuccessTotal != 0 ||
		frame.Stats.ErrorTotal != nil || frame.Stats.P50MS == nil || *frame.Stats.P50MS != 47.5 ||
		frame.Stats.P95MS != nil || frame.ObservedRevision == nil || *frame.ObservedRevision != 3 ||
		frame.ObservedHash != "abc123" {
		t.Fatalf("optional status projection changed on ingress: %+v", frame)
	}

	opts := paho.NewClientOptions().
		AddBroker("tcp://" + e.addr).
		SetClientID("gw-mqtt-connector-prehello").
		SetUsername("gw").
		SetPassword("pw").
		SetAutoReconnect(false)
	prehello := paho.NewClient(opts)
	if token := prehello.Connect(); token.Wait() && token.Error() != nil {
		t.Fatalf("pre-hello connect: %v", token.Error())
	}
	t.Cleanup(func() { prehello.Disconnect(50) })
	token = prehello.Publish(mqttapi.TopicGatewayConnectorStatus, 1, false, valid)
	if token.Wait() && token.Error() != nil {
		t.Fatalf("publish pre-hello status: %v", token.Error())
	}
	time.Sleep(150 * time.Millisecond)
	if receiver.count() != 1 {
		t.Fatal("pre-hello connector status reached the connector service")
	}
}

func TestE2EConnectorSpecAndDeleteRetainExactFrames(t *testing.T) {
	e := startE2E(t)
	messages := make(chan paho.Message, 2)
	if token := e.client.Subscribe(mqttapi.FilterGatewayConnectors, 1, func(_ paho.Client, message paho.Message) {
		messages <- message
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe connectors: %v", token.Error())
	}

	frames := []connector.PushFrame{
		{
			Kind: connector.KindSpec, TenantID: "t", GatewayID: "gw", SpecID: "s1", DeviceID: "sub1",
			ConnectorType: "edge-manager-resource", TypeVersion: "1",
			Config:   json.RawMessage(`{"manager_device":"nereid-inference","resource":"PipelinesJSON","pipeline_id":"p1"}`),
			Mappings: json.RawMessage(`[]`), Revision: 3,
		},
		{Kind: connector.KindDelete, TenantID: "t", GatewayID: "gw", SpecID: "s1", DeviceID: "sub1", Revision: 3},
	}
	for _, frame := range frames {
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		e.relay.handleConnectorDesired(&nats.Msg{Data: raw})
		select {
		case message := <-messages:
			wantTopic := mqttapi.TopicGatewayConnectorSpec
			if frame.Kind == connector.KindDelete {
				wantTopic = mqttapi.TopicGatewayConnectorDelete
			}
			if message.Topic() != wantTopic {
				t.Fatalf("kind %s topic=%q want %q", frame.Kind, message.Topic(), wantTopic)
			}
			var got connector.PushFrame
			if err := json.Unmarshal(message.Payload(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Kind != frame.Kind || got.TenantID != frame.TenantID || got.GatewayID != frame.GatewayID ||
				got.SpecID != frame.SpecID || got.DeviceID != frame.DeviceID || got.Revision != frame.Revision ||
				got.ConnectorType != frame.ConnectorType || got.TypeVersion != frame.TypeVersion ||
				string(got.Config) != string(frame.Config) || string(got.Mappings) != string(frame.Mappings) {
				t.Fatalf("wire frame changed: got=%+v want=%+v", got, frame)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for connector %s", frame.Kind)
		}
	}
}
