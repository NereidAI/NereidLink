// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_stateful_test.go verifies event-driven timing, reset behavior, bounded
// keys, concurrent one-shot emission, and graph propagation of True/False edges.

package rule

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stateMessage creates one keyed upstream True/False event.
func stateMessage(key, relation string) Msg {
	msg := Msg{Data: map[string]any{"device": map[string]any{"id": key}}}
	return messageWithInputRelation(msg, relation)
}

// newTestDebounce constructs a concrete node with a controllable clock.
func newTestDebounce(t *testing.T, raw string) *debounceNode {
	t.Helper()
	built, err := newDebounceNode("debounce", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("build debounce: %v", err)
	}
	return built.(*debounceNode)
}

// newTestSustained constructs a concrete node with a controllable clock.
func newTestSustained(t *testing.T, raw string) *sustainedNode {
	t.Helper()
	built, err := newSustainedNode("sustained", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("build sustained: %v", err)
	}
	return built.(*sustainedNode)
}

// TestDebounceIntervalKeysAndReset verifies throttling, key isolation, and False reset.
func TestDebounceIntervalKeysAndReset(t *testing.T) {
	node := newTestDebounce(t, `{"key_fields":["device.id"],"interval_ms":100}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }

	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("first True = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationFalse {
		t.Fatalf("immediate repeat = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationTrue {
		t.Fatalf("independent key = %q", got)
	}
	now = now.Add(100 * time.Millisecond)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("interval boundary = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationFalse)); got != RelationFalse {
		t.Fatalf("False passthrough = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("True after reset = %q", got)
	}
}

// TestDebounceBounds verifies deterministic rejection and capacity recovery.
func TestDebounceBounds(t *testing.T) {
	node := newTestDebounce(t, `{"key_fields":["device.id"],"interval_ms":100,"max_keys":1}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }

	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("first key = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationFalse {
		t.Fatalf("new key at capacity = %q", got)
	}
	now = now.Add(time.Second)
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationFalse {
		t.Fatalf("rejected key must remain untracked = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("existing key at capacity = %q", got)
	}
	executeRelation(t, node, stateMessage("a", RelationFalse))
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationTrue {
		t.Fatalf("new key after reset = %q", got)
	}
}

// TestDebounceConcurrentKey verifies that concurrent repeats emit only once.
func TestDebounceConcurrentKey(t *testing.T) {
	node := newTestDebounce(t, `{"key_fields":["device.id"],"interval_ms":1000}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }

	const goroutines = 64
	start := make(chan struct{})
	var emitted atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			branches, err := node.Execute(context.Background(), stateMessage("same", RelationTrue), false)
			if err != nil {
				t.Errorf("execute: %v", err)
				return
			}
			if len(branches) == 1 && branches[0].Relation == RelationTrue {
				emitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := emitted.Load(); got != 1 {
		t.Fatalf("True emissions = %d, want 1", got)
	}
}

// TestSustainedEventDriven verifies delayed one-shot emission and reset without timers.
func TestSustainedEventDriven(t *testing.T) {
	node := newTestSustained(t, `{"key_fields":["device.id"],"duration_ms":250}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }

	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationFalse {
		t.Fatalf("initial True = %q", got)
	}
	now = now.Add(249 * time.Millisecond)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationFalse {
		t.Fatalf("before duration = %q", got)
	}
	now = now.Add(time.Millisecond)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("duration boundary = %q", got)
	}
	now = now.Add(time.Second)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationFalse {
		t.Fatalf("post-emission suppression = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationFalse)); got != RelationFalse {
		t.Fatalf("False reset = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationFalse {
		t.Fatalf("restart after reset = %q", got)
	}
	now = now.Add(250 * time.Millisecond)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("emission after reset = %q", got)
	}
}

// TestSustainedBounds verifies capacity rejection and reset-driven recovery.
func TestSustainedBounds(t *testing.T) {
	node := newTestSustained(t, `{"key_fields":["device.id"],"duration_ms":100,"max_keys":1}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }

	executeRelation(t, node, stateMessage("a", RelationTrue))
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationFalse {
		t.Fatalf("new key at capacity = %q", got)
	}
	now = now.Add(100 * time.Millisecond)
	if got := executeRelation(t, node, stateMessage("a", RelationTrue)); got != RelationTrue {
		t.Fatalf("existing key at capacity = %q", got)
	}
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationFalse {
		t.Fatalf("rejected key must remain untracked = %q", got)
	}
	executeRelation(t, node, stateMessage("a", RelationFalse))
	executeRelation(t, node, stateMessage("b", RelationTrue))
	now = now.Add(100 * time.Millisecond)
	if got := executeRelation(t, node, stateMessage("b", RelationTrue)); got != RelationTrue {
		t.Fatalf("new key after reset = %q", got)
	}
}

// TestSustainedConcurrentBoundary verifies one emission at the elapsed boundary.
func TestSustainedConcurrentBoundary(t *testing.T) {
	node := newTestSustained(t, `{"key_fields":["device.id"],"duration_ms":100}`)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	node.now = func() time.Time { return now }
	executeRelation(t, node, stateMessage("same", RelationTrue))
	now = now.Add(100 * time.Millisecond)

	const goroutines = 64
	start := make(chan struct{})
	var emitted atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			branches, err := node.Execute(context.Background(), stateMessage("same", RelationTrue), false)
			if err != nil {
				t.Errorf("execute: %v", err)
				return
			}
			if len(branches) == 1 && branches[0].Relation == RelationTrue {
				emitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := emitted.Load(); got != 1 {
		t.Fatalf("True emissions = %d, want 1", got)
	}
}

// TestStatefulBuildValidation covers closed configs, defaults, and numeric bounds.
func TestStatefulBuildValidation(t *testing.T) {
	for _, test := range []struct {
		nodeType string
		raw      string
	}{
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":0}`},
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":-1}`},
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":1,"max_keys":-1}`},
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":1,"key_fields":["a..b"]}`},
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":1,"script":"return true"}`},
		{nodeType: NodeTypeDebounce, raw: `{"interval_ms":9223372036854775807}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":0}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":-1}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":1,"max_keys":-1}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":1,"key_fields":[""]}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":1,"expression":"elapsed"}`},
		{nodeType: NodeTypeSustained, raw: `{"duration_ms":9223372036854775807}`},
	} {
		t.Run(test.nodeType+test.raw, func(t *testing.T) {
			if _, err := buildNode("state", test.nodeType, json.RawMessage(test.raw), Deps{}); err == nil {
				t.Fatal("expected build error")
			}
		})
	}

	debounce := newTestDebounce(t, `{"interval_ms":1}`)
	if debounce.config.MaxKeys != defaultMaxStateKeys {
		t.Fatalf("debounce default max_keys = %d", debounce.config.MaxKeys)
	}
	sustained := newTestSustained(t, `{"duration_ms":1}`)
	if sustained.config.MaxKeys != defaultMaxStateKeys {
		t.Fatalf("sustained default max_keys = %d", sustained.config.MaxKeys)
	}
}

// TestStatefulGraphRelations proves that both filter edges reach and reset state.
func TestStatefulGraphRelations(t *testing.T) {
	graph, err := Compile("chain", GraphDTO{
		Nodes: []NodeDTO{
			{ID: "filter", Type: NodeTypeDeclarativeFilter, Config: json.RawMessage(
				`{"all":[{"field":"temperature","op":"gte","value":20}]}`)},
			{ID: "sustained", Type: NodeTypeSustained, Config: json.RawMessage(
				`{"key_fields":["device"],"duration_ms":100}`)},
		},
		Edges: []EdgeDTO{
			{From: "filter", To: "sustained", Relation: RelationTrue},
			{From: "filter", To: "sustained", Relation: RelationFalse},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	sustained := graph.nodes["sustained"].(*sustainedNode)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	sustained.now = func() time.Time { return now }

	execute := func(temperature int) string {
		t.Helper()
		trace, err := graph.Execute(context.Background(), Msg{Data: map[string]any{
			"device": "a", "temperature": temperature,
		}}, false, nil)
		if err != nil {
			t.Fatalf("execute graph: %v", err)
		}
		if len(trace.Records) != 2 || len(trace.Records[1].Out) != 1 {
			t.Fatalf("unexpected trace: %+v", trace.Records)
		}
		return trace.Records[1].Out[0].Relation
	}
	if got := execute(20); got != RelationFalse {
		t.Fatalf("initial sustained output = %q", got)
	}
	now = now.Add(100 * time.Millisecond)
	if got := execute(21); got != RelationTrue {
		t.Fatalf("elapsed sustained output = %q", got)
	}
	if got := execute(10); got != RelationFalse {
		t.Fatalf("upstream False output = %q", got)
	}
	if got := execute(22); got != RelationFalse {
		t.Fatalf("post-reset sustained output = %q", got)
	}
}
