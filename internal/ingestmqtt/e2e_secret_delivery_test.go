// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// e2e_secret_delivery_test.go drives the full secret delivery path over a
// real loopback TCP MQTT socket: the Core NATS desired frame carries only
// secret:// field references, the relay opens the typed vault documents at
// the last mile and substitutes plaintext into v1/gateway/secrets/sync, and
// the edge status uplink on v1/gateway/secrets/status reaches the binding
// service (ADR-0034).

package ingestmqtt

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/secretbinding"
)

// fakeDeliveryDocs is the last-mile vault: it returns one opened typed
// document per requested name.
type fakeDeliveryDocs struct {
	docs map[string]map[string]any
}

func (f *fakeDeliveryDocs) ResolveDocuments(_ context.Context, _ string, names []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, name := range names {
		if doc, ok := f.docs[name]; ok {
			out[name] = doc
		}
	}
	return out, nil
}

// fakeDispatchMarker records the revisions marked DISPATCHED.
type fakeDispatchMarker struct {
	mu        sync.Mutex
	revisions []int64
}

func (f *fakeDispatchMarker) MarkDispatched(_ context.Context, _, _ string, revisions []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revisions = append(f.revisions, revisions...)
	return nil
}

func (f *fakeDispatchMarker) values() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.revisions...)
}

// fakeSecretStatusReceiver records the edge status frames handed past the
// publish hook.
type fakeSecretStatusReceiver struct {
	mu     sync.Mutex
	frames []secretbinding.StatusFrame
}

func (f *fakeSecretStatusReceiver) HandleStatus(_ context.Context, _, _ string, frame secretbinding.StatusFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, frame)
	return nil
}

func (f *fakeSecretStatusReceiver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.frames)
}

// TestE2ESecretDeliveryLastMileAndStatus proves both directions over the
// wire: plaintext exists only in the single MQTT sync packet, and an APPLIED
// report flows back to the binding service.
func TestE2ESecretDeliveryLastMileAndStatus(t *testing.T) {
	e := startE2E(t)

	syncCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewaySecretsSync, 1, func(_ paho.Client, m paho.Message) {
		syncCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe secrets sync: %v", token.Error())
	}

	// The negotiated session supports secrets; an older firmware would have
	// no features and the relay would skip the write.
	e.hooks.markFeatures("gw-mqtt", []string{edge.FeatureSecrets})

	docs := &fakeDeliveryDocs{docs: map[string]map[string]any{
		"cam1": {"password": "s3cr3t"},
	}}
	marker := &fakeDispatchMarker{}
	e.relay.SetDeliverySecretResolver(docs, marker)

	frame := secretbinding.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []secretbinding.Entry{{
			Name:      "cam1",
			Revision:  3,
			Selectors: []secretbinding.Selector{{Kind: "mac", Value: "AA:BB:CC:00:11:22"}},
			Document: map[string]any{
				"username": "camera",
				"password": "secret://cam1#password",
			},
		}},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	e.relay.handleSecretDesired(&nats.Msg{Data: raw})

	select {
	case m := <-syncCh:
		if m.Topic() != mqttapi.TopicGatewaySecretsSync {
			t.Fatalf("unexpected topic %q", m.Topic())
		}
		var got secretbinding.PushFrame
		if err := json.Unmarshal(m.Payload(), &got); err != nil {
			t.Fatalf("decode sync frame: %v", err)
		}
		if len(got.Entries) != 1 {
			t.Fatalf("expected one entry, got %d", len(got.Entries))
		}
		entry := got.Entries[0]
		if entry.Document["username"] != "camera" {
			t.Fatalf("username not delivered: %+v", entry.Document)
		}
		if entry.Document["password"] != "s3cr3t" {
			t.Fatalf("plaintext must be substituted at the last mile, got %v", entry.Document["password"])
		}
		if len(entry.Selectors) != 1 || entry.Selectors[0].Value != "AA:BB:CC:00:11:22" {
			t.Fatalf("selectors must be delivered verbatim: %+v", entry.Selectors)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/secrets/sync")
	}

	if revisions := marker.values(); len(revisions) != 1 || revisions[0] != 3 {
		t.Fatalf("revision 3 must be marked DISPATCHED, got %v", revisions)
	}

	// Edge -> NL: the gateway reports APPLIED; the frame must cross the
	// publish hook into the binding receiver with tenant/gateway taken from
	// the authenticated session.
	receiver := &fakeSecretStatusReceiver{}
	e.publishHook.SetSecretStatusReceiver(receiver)

	status := secretbinding.StatusFrame{Reports: []secretbinding.StatusEntry{{
		Name: "cam1", Revision: 3, Status: secretbinding.StatusApplied,
	}}}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	pub := e.client.Publish(mqttapi.TopicGatewaySecretsStatus, 1, false, rawStatus)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish status: %v", pub.Error())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && receiver.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if receiver.count() != 1 {
		t.Fatal("edge status frame must reach the binding receiver")
	}
}

// TestE2ESecretDeliveryNotSentWithoutFeature ensures the negotiated feature
// gate holds over the real broker: with the secrets feature absent the relay
// writes nothing, even though the binding row exists.
func TestE2ESecretDeliveryNotSentWithoutFeature(t *testing.T) {
	e := startE2E(t)

	syncCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewaySecretsSync, 1, func(_ paho.Client, m paho.Message) {
		syncCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe secrets sync: %v", token.Error())
	}

	// Firmware without the secrets feature (only base telemetry).
	e.hooks.markFeatures("gw-mqtt", []string{edge.FeatureTelemetry})
	e.relay.SetDeliverySecretResolver(&fakeDeliveryDocs{docs: map[string]map[string]any{
		"cam1": {"password": "s3cr3t"},
	}}, &fakeDispatchMarker{})

	raw, err := json.Marshal(secretbinding.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []secretbinding.Entry{{
			Name: "cam1", Revision: 1,
			Document: map[string]any{"password": "secret://cam1#password"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.relay.handleSecretDesired(&nats.Msg{Data: raw})

	select {
	case <-syncCh:
		t.Fatal("unsupported firmware must not receive the sync frame")
	case <-time.After(300 * time.Millisecond):
	}
}
