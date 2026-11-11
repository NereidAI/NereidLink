// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// e2e_profile_delivery_test.go drives the device-profile downlink over a real
// loopback TCP MQTT socket: a Core NATS desired frame is forwarded to the
// owning gateway on v1/gateway/profiles/spec, the negotiated "profiles"
// feature gates the write, PENDING rows are marked DISPATCHED after the write,
// and an edge status uplink on v1/gateway/profiles/status reaches the delivery
// service (ADR-0038).

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
	"github.com/nereidai/nereidlink/internal/profilepush"
)

// fakeProfileMarker records the (profile, revision) pairs marked DISPATCHED.
type fakeProfileMarker struct {
	mu      sync.Mutex
	entries []profileDispatch
}

type profileDispatch struct {
	profileID string
	revision  int64
}

func (f *fakeProfileMarker) MarkDispatched(_ context.Context, _, _, profileID string, revision int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, profileDispatch{profileID: profileID, revision: revision})
	return nil
}

func (f *fakeProfileMarker) values() []profileDispatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]profileDispatch(nil), f.entries...)
}

// fakeProfileStatusReceiver records the edge status frames handed past the
// publish hook.
type fakeProfileStatusReceiver struct {
	mu     sync.Mutex
	frames []profilepush.StatusFrame
}

func (f *fakeProfileStatusReceiver) HandleStatus(_ context.Context, _, _ string, frame profilepush.StatusFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, frame)
	return nil
}

func (f *fakeProfileStatusReceiver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.frames)
}

// TestE2EProfileDeliveryLastMileAndStatus proves both directions over the
// wire: a rendered profile frame reaches the gateway and an APPLIED report
// flows back to the delivery service.
func TestE2EProfileDeliveryLastMileAndStatus(t *testing.T) {
	e := startE2E(t)

	specCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayProfilesSpec, 1, func(_ paho.Client, m paho.Message) {
		specCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe profiles spec: %v", token.Error())
	}

	// The negotiated session supports profiles; an older firmware would have
	// no features and the relay would skip the write.
	e.hooks.markFeatures("gw-mqtt", []string{edge.FeatureProfiles})

	marker := &fakeProfileMarker{}
	e.relay.SetProfileDispatchMarker(marker)

	frame := profilepush.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []profilepush.Entry{{
			ProfileID: "p1",
			Name:      "A1",
			Revision:  2,
			Format:    profilepush.FormatNeutral,
			Document: map[string]any{
				"name":   "A1",
				"points": []any{map[string]any{"name": "temperature", "type": "float"}},
			},
		}},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	e.relay.handleProfileDesired(&nats.Msg{Data: raw})

	select {
	case m := <-specCh:
		if m.Topic() != mqttapi.TopicGatewayProfilesSpec {
			t.Fatalf("unexpected topic %q", m.Topic())
		}
		var got profilepush.PushFrame
		if err := json.Unmarshal(m.Payload(), &got); err != nil {
			t.Fatalf("decode spec frame: %v", err)
		}
		if len(got.Entries) != 1 {
			t.Fatalf("expected one entry, got %d", len(got.Entries))
		}
		entry := got.Entries[0]
		if entry.Name != "A1" || entry.ProfileID != "p1" || entry.Revision != 2 {
			t.Fatalf("entry identity altered in transit: %+v", entry)
		}
		if entry.Document["name"] != "A1" {
			t.Fatalf("document must be forwarded verbatim: %+v", entry.Document)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/profiles/spec")
	}

	dispatched := marker.values()
	if len(dispatched) != 1 || dispatched[0].profileID != "p1" || dispatched[0].revision != 2 {
		t.Fatalf("(p1,2) must be marked DISPATCHED, got %+v", dispatched)
	}

	// Edge -> NL: the gateway reports APPLIED; the frame must cross the
	// publish hook into the delivery receiver with tenant/gateway taken from
	// the authenticated session.
	receiver := &fakeProfileStatusReceiver{}
	e.publishHook.SetProfileStatusReceiver(receiver)

	status := profilepush.StatusFrame{Reports: []profilepush.StatusEntry{{
		Name: "A1", Revision: 2, Status: profilepush.StateApplied,
	}}}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	pub := e.client.Publish(mqttapi.TopicGatewayProfilesStatus, 1, false, rawStatus)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish status: %v", pub.Error())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && receiver.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if receiver.count() != 1 {
		t.Fatal("edge status frame must reach the delivery receiver")
	}
}

// TestE2EProfileDeliveryNotSentWithoutFeature ensures the negotiated feature
// gate holds over the real broker: with the profiles feature absent the relay
// writes nothing and marks nothing, even though a delivery row exists.
func TestE2EProfileDeliveryNotSentWithoutFeature(t *testing.T) {
	e := startE2E(t)

	specCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayProfilesSpec, 1, func(_ paho.Client, m paho.Message) {
		specCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe profiles spec: %v", token.Error())
	}

	// Firmware without the profiles feature.
	e.hooks.markFeatures("gw-mqtt", []string{edge.FeatureTelemetry})
	marker := &fakeProfileMarker{}
	e.relay.SetProfileDispatchMarker(marker)

	raw, err := json.Marshal(profilepush.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []profilepush.Entry{{
			ProfileID: "p1", Name: "A1", Revision: 1,
			Format:   profilepush.FormatNeutral,
			Document: map[string]any{"name": "A1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.relay.handleProfileDesired(&nats.Msg{Data: raw})

	select {
	case <-specCh:
		t.Fatal("unsupported firmware must not receive the spec frame")
	case <-time.After(300 * time.Millisecond):
	}
	if len(marker.values()) != 0 {
		t.Fatal("unsupported firmware must not have rows marked DISPATCHED")
	}
}

// TestE2EProfileRemovalFrameCarriesNoDocument asserts an explicit removal entry
// is forwarded without a document and is not marked DISPATCHED.
func TestE2EProfileRemovalFrameCarriesNoDocument(t *testing.T) {
	e := startE2E(t)

	specCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayProfilesSpec, 1, func(_ paho.Client, m paho.Message) {
		specCh <- m
	}); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe profiles spec: %v", token.Error())
	}
	e.hooks.markFeatures("gw-mqtt", []string{edge.FeatureProfiles})
	marker := &fakeProfileMarker{}
	e.relay.SetProfileDispatchMarker(marker)

	raw, err := json.Marshal(profilepush.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []profilepush.Entry{{
			ProfileID: "p2", Name: "Old", Revision: 1,
			Format:   profilepush.FormatNeutral,
			Removing: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.relay.handleProfileDesired(&nats.Msg{Data: raw})

	select {
	case m := <-specCh:
		var got profilepush.PushFrame
		if err := json.Unmarshal(m.Payload(), &got); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if len(got.Entries) != 1 || !got.Entries[0].Removing {
			t.Fatalf("removal entry not forwarded: %+v", got.Entries)
		}
		if got.Entries[0].Document != nil {
			t.Fatal("removal entry must not carry a document")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removal frame")
	}
	if len(marker.values()) != 0 {
		t.Fatal("removal entries must not be marked DISPATCHED")
	}
}
