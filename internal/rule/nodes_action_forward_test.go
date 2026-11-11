// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_action_forward_test.go verifies declarative command routing and static
// parameters, script compatibility, and lossless/selective forward envelopes.

package rule

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// commandCall records one command dispatch for assertions.
type commandCall struct {
	tenantID  string
	deviceID  string
	name      string
	params    map[string]any
	timeoutMS int64
}

// recordingCommandDispatcher implements CommandDispatcher in memory.
type recordingCommandDispatcher struct {
	calls []commandCall
}

// DispatchCommand records the invocation and returns a successful status.
func (d *recordingCommandDispatcher) DispatchCommand(_ context.Context, tenantID, deviceID, name string, params map[string]any, timeoutMS int64) (string, error) {
	d.calls = append(d.calls, commandCall{
		tenantID: tenantID, deviceID: deviceID, name: name, params: params, timeoutMS: timeoutMS,
	})
	return "SENT", nil
}

// objectScripter implements Scripter for command compatibility tests.
type objectScripter struct {
	result map[string]any
	calls  int
}

// EvalBoolean is unused by Send Command but completes the Scripter interface.
func (s *objectScripter) EvalBoolean(string, map[string]any, map[string]any) (bool, error) {
	return false, nil
}

// EvalObject returns configured script parameters and records script use.
func (s *objectScripter) EvalObject(string, map[string]any, map[string]any) (map[string]any, error) {
	s.calls++
	return s.result, nil
}

// TestSendCommandStaticTargetAndParams verifies declarative dispatch fields.
func TestSendCommandStaticTargetAndParams(t *testing.T) {
	dispatcher := &recordingCommandDispatcher{}
	node, err := buildNode("command", NodeTypeSendCommand, json.RawMessage(`{
		"command_name":"restart",
		"target_device_id":"target-device",
		"params":{"delay":5,"force":true},
		"timeout_ms":2500
	}`), Deps{Commands: dispatcher})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	in := Msg{TenantID: "tenant", DeviceID: "source-device", Data: map[string]any{}}
	if got := executeRelation(t, node, in); got != RelationSuccess {
		t.Fatalf("relation = %q", got)
	}
	want := commandCall{
		tenantID: "tenant", deviceID: "target-device", name: "restart",
		params: map[string]any{"delay": float64(5), "force": true}, timeoutMS: 2500,
	}
	if len(dispatcher.calls) != 1 || !reflect.DeepEqual(dispatcher.calls[0], want) {
		t.Fatalf("calls = %+v, want %+v", dispatcher.calls, want)
	}
}

// TestSendCommandScriptCompatibility verifies script params and original device fallback.
func TestSendCommandScriptCompatibility(t *testing.T) {
	dispatcher := &recordingCommandDispatcher{}
	script := &objectScripter{result: map[string]any{"from_script": true}}
	node, err := buildNode("command", NodeTypeSendCommand, json.RawMessage(`{
		"command_name":"configure",
		"params_script":"return {from_script: true};"
	}`), Deps{Commands: dispatcher, Script: script})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := executeRelation(t, node, Msg{TenantID: "tenant", DeviceID: "message-device"}); got != RelationSuccess {
		t.Fatalf("relation = %q", got)
	}
	if script.calls != 1 {
		t.Fatalf("script calls = %d, want 1", script.calls)
	}
	if len(dispatcher.calls) != 1 || dispatcher.calls[0].deviceID != "message-device" ||
		!reflect.DeepEqual(dispatcher.calls[0].params, script.result) {
		t.Fatalf("unexpected calls: %+v", dispatcher.calls)
	}
}

// TestSendCommandRejectsDualParams enforces mutual exclusion at build time.
func TestSendCommandRejectsDualParams(t *testing.T) {
	for _, raw := range []string{
		`{"params":{"value":1},"params_script":"return {};"}`,
		`{"params":{},"params_script":"return {};"}`,
	} {
		if _, err := buildNode("command", NodeTypeSendCommand, json.RawMessage(raw), Deps{}); err == nil {
			t.Fatalf("expected dual-params error for %s", raw)
		}
	}
}

// recordingEgressPublisher implements EgressPublisher in memory.
type recordingEgressPublisher struct {
	forwarderID string
	input       ForwardInput
	calls       int
}

// PublishForward records one complete envelope.
func (p *recordingEgressPublisher) PublishForward(_ context.Context, forwarderID string, in ForwardInput) error {
	p.forwarderID = forwarderID
	p.input = in
	p.calls++
	return nil
}

// TestForwardWhitelist verifies nested shape preservation and chain immutability.
func TestForwardWhitelist(t *testing.T) {
	publisher := &recordingEgressPublisher{}
	node, err := buildNode("forward", NodeTypeForward, json.RawMessage(`{
		"forwarder_id":"egress",
		"fields":["temperature","nested.value","missing"]
	}`), Deps{Egress: publisher})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	data := map[string]any{
		"temperature": 22,
		"humidity":    50,
		"nested":      map[string]any{"value": "kept", "secret": "omitted"},
		"embedding":   []any{0.1, 0.2},
	}
	branches, err := node.Execute(context.Background(), Msg{Data: data}, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := map[string]any{
		"temperature": 22,
		"nested":      map[string]any{"value": "kept"},
	}
	if publisher.calls != 1 || publisher.forwarderID != "egress" || !reflect.DeepEqual(publisher.input.Data, want) {
		t.Fatalf("published data = %#v, want %#v", publisher.input.Data, want)
	}
	if len(branches) != 1 || !reflect.DeepEqual(branches[0].Msg.Data, data) {
		t.Fatal("forward must pass the original full message onward")
	}
	if !reflect.DeepEqual(data["nested"], map[string]any{"value": "kept", "secret": "omitted"}) {
		t.Fatal("whitelist construction mutated source data")
	}
}

func TestForwardCarriesBusinessChannel(t *testing.T) {
	publisher := &recordingEgressPublisher{}
	node, err := buildNode("forward", NodeTypeForward, json.RawMessage(`{
		"forwarder_id":"egress",
		"channel":"meter-reading.v1",
		"fields":["value"]
	}`), Deps{Egress: publisher})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = node.Execute(context.Background(), Msg{
		TenantID: "tenant", DeviceID: "device", TraceID: "trace",
		Data: map[string]any{"value": 1.5, "secret": "redacted"},
	}, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if publisher.forwarderID != "egress" || publisher.input.Channel != "meter-reading.v1" {
		t.Fatalf("channel envelope = %+v forwarder=%q", publisher.input, publisher.forwarderID)
	}
}

// TestForwardWithoutWhitelistPreservesEmbeddings verifies the full-message default.
func TestForwardWithoutWhitelistPreservesEmbeddings(t *testing.T) {
	for _, fields := range []string{"", `,"fields":[]`} {
		t.Run(fields, func(t *testing.T) {
			publisher := &recordingEgressPublisher{}
			node, err := buildNode("forward", NodeTypeForward, json.RawMessage(
				`{"forwarder_id":"egress"`+fields+`}`), Deps{Egress: publisher})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			data := map[string]any{
				"temperature": 22,
				"embedding":   []any{0.1, 0.2},
				"nested":      map[string]any{"all": true},
			}
			executeRelation(t, node, Msg{Data: data})
			if !reflect.DeepEqual(publisher.input.Data, data) {
				t.Fatalf("published data = %#v, want complete %#v", publisher.input.Data, data)
			}
		})
	}
}

// TestForwardExplicitEmbeddingWhitelist verifies that selection is explicit, not type-based.
func TestForwardExplicitEmbeddingWhitelist(t *testing.T) {
	publisher := &recordingEgressPublisher{}
	node, err := buildNode("forward", NodeTypeForward, json.RawMessage(
		`{"forwarder_id":"egress","fields":["embedding"]}`), Deps{Egress: publisher})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	embedding := []any{0.1, 0.2}
	executeRelation(t, node, Msg{Data: map[string]any{"embedding": embedding, "other": true}})
	if !reflect.DeepEqual(publisher.input.Data, map[string]any{"embedding": embedding}) {
		t.Fatalf("published data = %#v", publisher.input.Data)
	}
}

// TestForwardWhitelistValidation rejects ambiguous paths during graph construction.
func TestForwardWhitelistValidation(t *testing.T) {
	for _, raw := range []string{
		`{"forwarder_id":"egress","fields":[""]}`,
		`{"forwarder_id":"egress","fields":["a..b"]}`,
		`{"forwarder_id":"egress","fields":[".a"]}`,
	} {
		if _, err := buildNode("forward", NodeTypeForward, json.RawMessage(raw), Deps{}); err == nil {
			t.Fatalf("expected whitelist error for %s", raw)
		}
	}
}
