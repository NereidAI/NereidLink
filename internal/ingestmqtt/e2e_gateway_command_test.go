// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// e2e_gateway_command_test.go drives the production MQTT broker hooks over a
// real loopback TCP socket with a Paho gateway client, proving the full
// gateway-relay path: downlink command on v1/gateway/command, downlink
// attribute push on v1/gateway/attributes/push, uplink command/response
// correlation, and uplink gateway attributes persistence.
package ingestmqtt

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochimqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/nats-io/nats.go"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/presence"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

func init() {
	// Keep development shell proxies from intercepting loopback MQTT.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy"} {
		_ = os.Unsetenv(key)
	}
}

type e2eAuthenticator struct{}

func (e2eAuthenticator) Authenticate(_ context.Context, username, _ string) (sdktelemetry.DeviceIdentity, bool, error) {
	if username == "gw" {
		return sdktelemetry.DeviceIdentity{
			TenantID:     "t",
			DeviceID:     "gw",
			Capabilities: []string{mqttapi.CapabilityGateway},
		}, true, nil
	}
	return sdktelemetry.DeviceIdentity{TenantID: "t", DeviceID: username}, true, nil
}

type e2ePresence struct{}

func (e2ePresence) Connect(context.Context, presence.Identity, string, string) (string, error) {
	return "sess", nil
}
func (e2ePresence) Disconnect(context.Context, presence.Identity, string) error { return nil }
func (e2ePresence) DisconnectGateway(context.Context, string, string) error     { return nil }
func (e2ePresence) EnsureGatewayOffline(context.Context, presence.Identity, string, string) (bool, error) {
	return false, nil
}

type e2eBroker struct {
	broker      *mochimqtt.Server
	hooks       *AuthHook
	publishHook *PublishHook
	relay       *CommandRelay
	saver       *fakeAttributeSaver
	addr        string
	cmdCh       chan paho.Message
	attrCh      chan paho.Message
	client      paho.Client
}

func freePortAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func startE2E(t *testing.T) *e2eBroker {
	t.Helper()
	addr := freePortAddr(t)
	broker := mochimqtt.New(nil)
	hooks := NewAuthHook(e2eAuthenticator{}, e2ePresence{}, "ing")
	if err := broker.AddHook(hooks, nil); err != nil {
		t.Fatalf("auth hook: %v", err)
	}
	relay := NewCommandRelay(nil, broker, hooks)
	publishHook := NewPublishHook(nil, hooks, e2ePresence{}, relay, nil)
	publishHook.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	saver := &fakeAttributeSaver{}
	publishHook.SetAttributeSaver(saver)
	if err := broker.AddHook(publishHook, nil); err != nil {
		t.Fatalf("publish hook: %v", err)
	}
	if err := broker.AddListener(listeners.NewTCP(listeners.Config{ID: "e2e-tcp", Address: addr})); err != nil {
		t.Fatalf("listener: %v", err)
	}
	go func() { _ = broker.Serve() }()
	t.Cleanup(func() { _ = broker.Close() })

	e := &e2eBroker{
		broker: broker, hooks: hooks, publishHook: publishHook, relay: relay,
		saver: saver, addr: addr,
		cmdCh: make(chan paho.Message, 4), attrCh: make(chan paho.Message, 4),
	}

	opts := paho.NewClientOptions().
		AddBroker("tcp://" + addr).
		SetClientID("gw-mqtt").
		SetUsername("gw").
		SetPassword("pw").
		SetAutoReconnect(false)
	opts.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		switch m.Topic() {
		case mqttapi.TopicGatewayCommand:
			e.cmdCh <- m
		case mqttapi.TopicGatewayAttributesPush:
			e.attrCh <- m
		}
	})
	client := paho.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		t.Fatalf("gateway connect: %v", token.Error())
	}
	t.Cleanup(func() { client.Disconnect(250) })
	e.client = client

	// Subscribe to the two gateway downlinks (ACL requires gateway cap).
	for _, topic := range []string{mqttapi.TopicGatewayCommand, mqttapi.TopicGatewayAttributesPush} {
		if token := client.Subscribe(topic, 1, nil); token.Wait() && token.Error() != nil {
			t.Fatalf("subscribe %s: %v", topic, token.Error())
		}
	}

	// Production path: only a negotiated gateway that attached sub1 may use it.
	hooks.markNegotiated("gw-mqtt", "edge-session")
	hooks.recordSubdevice("gw-mqtt", "t", "sub1")
	return e
}

func TestE2EGatewayCommandRoundTrip(t *testing.T) {
	e := startE2E(t)

	// 1) NL -> Edge: dispatch a request-response command for the sub-device.
	wire := &v1.Command{
		Id: "cmd-e2e-1", TenantId: "t", DeviceId: "sub1", GatewayId: "gw",
		Name: "reboot", Params: []byte(`{"delay":1}`),
		Interaction: command.InteractionRequestResponse,
		Timeliness:  command.TimelinessRealtime,
	}
	e.relay.handleGatewayDispatch(wire, "")

	select {
	case m := <-e.cmdCh:
		if m.Topic() != mqttapi.TopicGatewayCommand {
			t.Fatalf("unexpected topic %q", m.Topic())
		}
		var frame gatewayCommandFrame
		if err := json.Unmarshal(m.Payload(), &frame); err != nil {
			t.Fatalf("decode command frame: %v", err)
		}
		if frame.CommandID != "cmd-e2e-1" || frame.Device != "sub1" ||
			frame.Method != "reboot" || string(frame.Params) != `{"delay":1}` {
			t.Fatalf("unexpected command frame: %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/command")
	}

	// 2) Edge -> NL: gateway returns the result; it must correlate and clear
	// the pending request registered by the dispatch.
	e.relay.mu.Lock()
	_, awaiting := e.relay.pending[wire.Id]
	e.relay.mu.Unlock()
	if !awaiting {
		t.Fatal("request-response command should be awaiting a response")
	}
	pub := e.client.Publish(mqttapi.TopicGatewayCommandResponse, 1, false,
		`{"command_id":"cmd-e2e-1","device":"sub1","data":{"ok":true}}`)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish response: %v", pub.Error())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.relay.mu.Lock()
		_, still := e.relay.pending[wire.Id]
		e.relay.mu.Unlock()
		if !still {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.relay.mu.Lock()
	_, still := e.relay.pending[wire.Id]
	e.relay.mu.Unlock()
	if still {
		t.Fatal("gateway command/response was not correlated; command still pending")
	}
}

func TestE2EGatewayCommandRejectsForeignSubdevice(t *testing.T) {
	e := startE2E(t)
	// sub2 is not attached to this gateway: dispatch must terminalize rather
	// than write a packet.
	wire := &v1.Command{
		Id: "cmd-e2e-2", TenantId: "t", DeviceId: "sub2", GatewayId: "gw",
		Name: "reboot", Interaction: command.InteractionOneWay,
	}
	_, _, ack := e.relay.resolveGatewayCommand(wire)
	if ack == nil || ack.Status != command.StatusNotConnected {
		t.Fatalf("foreign sub-device must be not-connected, got %+v", ack)
	}
	// An uplink response for an unattached device must be dropped, not
	// correlated to any command.
	pub := e.client.Publish(mqttapi.TopicGatewayCommandResponse, 1, false,
		`{"command_id":"cmd-e2e-2","device":"sub2","data":{}}`)
	pub.Wait()
	// Give the broker a moment to (incorrectly) process it.
	time.Sleep(100 * time.Millisecond)
	if len(e.saver.calls) != 0 {
		t.Fatalf("unexpected side effects: %+v", e.saver.calls)
	}
}

func TestE2EAttributePushAndUplink(t *testing.T) {
	e := startE2E(t)

	// Downlink: a SHARED_SCOPE attribute change reaches the gateway naming sub1.
	push := mqttapi.AttributePushFrame{
		TenantID: "t", DeviceID: "sub1", Scope: "SHARED_SCOPE",
		Key: "maxTemp", Value: json.RawMessage(`42`),
	}
	raw, _ := json.Marshal(push)
	e.relay.handleAttributePush(&nats.Msg{Data: raw})

	select {
	case m := <-e.attrCh:
		var frame mqttapi.GatewayAttributesPushFrame
		if err := json.Unmarshal(m.Payload(), &frame); err != nil {
			t.Fatalf("decode push frame: %v", err)
		}
		if frame.Device != "sub1" || frame.Scope != "SHARED_SCOPE" ||
			string(frame.Data["maxTemp"]) != "42" {
			t.Fatalf("unexpected push frame: %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v1/gateway/attributes/push")
	}

	// Uplink: gateway reports CLIENT-scope attributes for the attached sub.
	pub := e.client.Publish(mqttapi.TopicGatewayAttributes, 1, false,
		`{"sub1":{"firmware":"1.2.3"}}`)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish attributes: %v", pub.Error())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(e.saver.calls) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(e.saver.calls) != 1 {
		t.Fatalf("expected 1 persisted attribute batch, got %+v", e.saver.calls)
	}
	got := e.saver.calls[0]
	if got.deviceID != "sub1" || got.scope != "CLIENT" || got.attrs["firmware"] != "1.2.3" {
		t.Fatalf("unexpected persisted attributes: %+v", got)
	}
}
