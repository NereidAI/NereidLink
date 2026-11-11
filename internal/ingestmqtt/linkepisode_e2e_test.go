// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// linkepisode_e2e_test.go drives edge episode reports over a real loopback
// MQTT socket: a negotiated gateway publishes v1/gateway/linkhealth and
// receives an ack per outcome (accepted / duplicate / rejected).

package ingestmqtt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/nereidai/nereidlink/internal/linkhealth"
	"github.com/nereidai/nereidlink/internal/linknotify"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// fakeLinkEpisodeStore is the controllable LinkEpisodeStore for the e2e
// path.
type fakeLinkEpisodeStore struct {
	duplicate bool
	matched   bool
	calls     int
}

func (f *fakeLinkEpisodeStore) ApplyEdgeEpisode(_ context.Context, _ linknotify.EdgeEpisode) (linknotify.ApplyResult, error) {
	f.calls++
	if f.duplicate {
		return linknotify.ApplyResult{Status: linknotify.ApplyDuplicate}, nil
	}
	return linknotify.ApplyResult{Status: linknotify.ApplyAccepted, MatchedLease: f.matched}, nil
}

// waitAck receives one link episode ack within the timeout.
func waitAck(t *testing.T, ch chan paho.Message) linkhealth.LinkEpisodeAck {
	t.Helper()
	select {
	case m := <-ch:
		var ack linkhealth.LinkEpisodeAck
		if err := json.Unmarshal(m.Payload(), &ack); err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		return ack
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for link episode ack")
		return linkhealth.LinkEpisodeAck{}
	}
}

// TestE2ELinkEpisodeAcks proves accepted, duplicate and rejected responses
// over the wire.
func TestE2ELinkEpisodeAcks(t *testing.T) {
	e := startE2E(t)

	ackCh := make(chan paho.Message, 4)
	if token := e.client.Subscribe(mqttapi.TopicGatewayLinkHealthAck, 1,
		func(_ paho.Client, m paho.Message) { ackCh <- m }); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe ack: %v", token.Error())
	}

	e.hooks.markNegotiated("gw-mqtt", "edge-session")
	store := &fakeLinkEpisodeStore{}
	e.publishHook.linkEpisodes = store
	e.publishHook.SetBroker(e.broker, nil)

	down := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	report := linkhealth.LinkEpisodeReport{
		EdgeID: "gw",
		DownAt: down,
		UpAt:   down.Add(5 * time.Second),
		Reason: linkhealth.EpisodeReasonTransportDown,
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	pub := e.client.Publish(mqttapi.TopicGatewayLinkHealth, 1, false, raw)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish episode: %v", pub.Error())
	}
	ack := waitAck(t, ackCh)
	if ack.Status != linkhealth.EpisodeAckAccepted || ack.EdgeID != "gw" {
		t.Fatalf("expected accepted ack, got %+v", ack)
	}
	if store.calls != 1 {
		t.Fatalf("episode must reach the store once, calls=%d", store.calls)
	}

	// Same down_at again while the store reports duplicate.
	store.duplicate = true
	pub2 := e.client.Publish(mqttapi.TopicGatewayLinkHealth, 1, false, raw)
	if pub2.Wait() && pub2.Error() != nil {
		t.Fatalf("republish episode: %v", pub2.Error())
	}
	ack2 := waitAck(t, ackCh)
	if ack2.Status != linkhealth.EpisodeAckDuplicate {
		t.Fatalf("expected duplicate ack, got %+v", ack2)
	}

	// A frame whose edge_id does not match the authenticated connection is
	// rejected (and must never reach the store).
	badCalls := store.calls
	bad := report
	bad.EdgeID = "someone-else"
	bad.DownAt = down.Add(10 * time.Second)
	badRaw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	pub3 := e.client.Publish(mqttapi.TopicGatewayLinkHealth, 1, false, badRaw)
	if pub3.Wait() && pub3.Error() != nil {
		t.Fatalf("publish bad episode: %v", pub3.Error())
	}
	ack3 := waitAck(t, ackCh)
	if ack3.Status != linkhealth.EpisodeAckRejected {
		t.Fatalf("expected rejected ack, got %+v", ack3)
	}
	if store.calls != badCalls {
		t.Fatal("rejected frame must not reach the store")
	}
}

// TestValidateLinkEpisode covers the field rules without a broker.
func TestValidateLinkEpisode(t *testing.T) {
	down := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	valid := linkhealth.LinkEpisodeReport{
		EdgeID: "gw", DownAt: down, UpAt: down.Add(time.Second),
		Reason: "transport_down",
	}
	if reason := validateLinkEpisode(valid, "gw"); reason != "" {
		t.Fatalf("valid frame rejected: %s", reason)
	}

	missingEdge := valid
	missingEdge.EdgeID = ""
	mismatch := valid
	mismatch.EdgeID = "other"
	missingDown := valid
	missingDown.DownAt = time.Time{}
	missingUp := valid
	missingUp.UpAt = time.Time{}
	upBeforeDown := valid
	upBeforeDown.UpAt = valid.DownAt.Add(-time.Second)
	missingReason := valid
	missingReason.Reason = ""

	cases := []struct {
		name  string
		frame linkhealth.LinkEpisodeReport
	}{
		{"missing edge_id", missingEdge},
		{"mismatched edge_id", mismatch},
		{"missing down_at", missingDown},
		{"missing up_at", missingUp},
		{"up before down", upBeforeDown},
		{"missing reason", missingReason},
	}
	for _, tc := range cases {
		if reason := validateLinkEpisode(tc.frame, "gw"); reason == "" {
			t.Errorf("%s: must be rejected", tc.name)
		}
	}
}

// TestLinkEpisodeRequiresNegotiation ensures an un-negotiated connection's
// episode report is dropped without touching the store or acking.
func TestLinkEpisodeRequiresNegotiation(t *testing.T) {
	e := startE2E(t)
	e.publishHook.linkEpisodes = &fakeLinkEpisodeStore{}

	ackCh := make(chan paho.Message, 1)
	if token := e.client.Subscribe(mqttapi.TopicGatewayLinkHealthAck, 1,
		func(_ paho.Client, m paho.Message) { ackCh <- m }); token.Wait() && token.Error() != nil {
		t.Fatalf("subscribe ack: %v", token.Error())
	}

	down := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	raw, _ := json.Marshal(linkhealth.LinkEpisodeReport{
		EdgeID: "gw", DownAt: down, UpAt: down.Add(time.Second),
		Reason: "transport_down",
	})
	pub := e.client.Publish(mqttapi.TopicGatewayLinkHealth, 1, false, raw)
	pub.Wait()

	select {
	case <-ackCh:
		t.Fatal("un-negotiated connection must not receive an ack")
	case <-time.After(300 * time.Millisecond):
	}
}
