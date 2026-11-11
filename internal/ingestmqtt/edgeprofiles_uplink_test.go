// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import (
	"context"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/nereidai/nereidlink/internal/edgeprofile"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// recordingEdgeProfileReceiver captures the session-derived identity of each
// v1/gateway/profiles ingest call.
type recordingEdgeProfileReceiver struct {
	mu     sync.Mutex
	calls  []recordedEdgeProfileCall
	failOn error
}

type recordedEdgeProfileCall struct {
	tenantID  string
	gatewayID string
	frame     edgeprofile.Frame
}

func (r *recordingEdgeProfileReceiver) Ingest(_ context.Context, tenantID, gatewayID string, frame edgeprofile.Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedEdgeProfileCall{tenantID, gatewayID, frame})
	return r.failOn
}

func TestGatewayProfilesIngest(t *testing.T) {
	e := startE2E(t)
	recv := &recordingEdgeProfileReceiver{}
	e.publishHook.SetEdgeProfileReceiver(recv)

	payload := `{"profiles":[{"name":"Modbus-Temp","deviceResources":[
		{"name":"temperature","valueType":"Float64","units":"C"},
		{"name":"running","valueType":"Bool"}]}]}`
	pub := e.client.Publish(mqttapi.TopicGatewayProfiles, 1, false, payload)
	if pub.Wait() && pub.Error() != nil {
		t.Fatalf("publish profiles: %v", pub.Error())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recv.mu.Lock()
		n := len(recv.calls)
		recv.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.calls) != 1 {
		t.Fatalf("expected 1 ingest call, got %+v", recv.calls)
	}
	call := recv.calls[0]
	// Identity must come from the authenticated session, never the frame.
	if call.tenantID != "t" || call.gatewayID != "gw" {
		t.Fatalf("session identity not used: %+v", call)
	}
	if len(call.frame.Profiles) != 1 || call.frame.Profiles[0].Name != "Modbus-Temp" ||
		len(call.frame.Profiles[0].DeviceResources) != 2 {
		t.Fatalf("frame not parsed: %+v", call.frame)
	}
}

func TestGatewayProfilesDroppedBeforeHello(t *testing.T) {
	e := startE2E(t)
	recv := &recordingEdgeProfileReceiver{}
	e.publishHook.SetEdgeProfileReceiver(recv)

	// A second, still-unnegotiated gateway connection must not populate the
	// cache even though it authenticates with the gateway capability.
	opts := paho.NewClientOptions().
		AddBroker("tcp://" + e.addr).
		SetClientID("gw-mqtt-preneg").
		SetUsername("gw").
		SetPassword("pw").
		SetAutoReconnect(false)
	pre := paho.NewClient(opts)
	if token := pre.Connect(); token.Wait() && token.Error() != nil {
		t.Fatalf("pre-hello connect: %v", token.Error())
	}
	t.Cleanup(func() { pre.Disconnect(50) })

	pub := pre.Publish(mqttapi.TopicGatewayProfiles, 1, false,
		`{"profiles":[{"name":"X","deviceResources":[]}]}`)
	pub.Wait()
	time.Sleep(150 * time.Millisecond)

	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.calls) != 0 {
		t.Fatalf("pre-hello profile report must be dropped, got %+v", recv.calls)
	}
}
