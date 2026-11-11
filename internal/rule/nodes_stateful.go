// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_stateful.go implements event-driven debounce and sustained-condition
// primitives. Their bounded maps are process-local, mutex-protected, and empty
// after process restarts or graph recompilation; no timers or persistence exist.

package rule

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	// defaultMaxStateKeys bounds each stateful node unless configured otherwise.
	defaultMaxStateKeys = 10000

	// inputRelationMetadataKey carries the incoming edge label through the
	// existing Node.Execute contract. Boolean nodes and script-control wrappers
	// maintain it on every outgoing message.
	inputRelationMetadataKey = "_nereidlink_rule_input_relation"
)

// relationMetadataNode preserves the existing Node interface while making the
// incoming True/False branch available to downstream stateful nodes.
type relationMetadataNode struct {
	Node
}

// Execute tags each outgoing message with its branch relation. The reserved
// metadata key is overwritten by every subsequent node execution.
func (n *relationMetadataNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	branches, err := n.Node.Execute(ctx, in, dryRun)
	for index := range branches {
		branches[index].Msg = messageWithInputRelation(branches[index].Msg, branches[index].Relation)
	}
	return branches, err
}

// booleanBranch constructs one True/False output and tags it for direct
// composition as well as graph execution through relationMetadataNode.
func booleanBranch(in Msg, matched bool) Branch {
	relation := RelationFalse
	if matched {
		relation = RelationTrue
	}
	return Branch{Relation: relation, Msg: messageWithInputRelation(in, relation)}
}

// messageWithInputRelation copies metadata before adding the reserved branch
// marker so sibling branches never share a mutation.
func messageWithInputRelation(in Msg, relation string) Msg {
	if current, ok := in.Metadata[inputRelationMetadataKey].(string); ok && current == relation {
		return in
	}
	metadata := make(map[string]any, len(in.Metadata)+1)
	for key, value := range in.Metadata {
		metadata[key] = value
	}
	metadata[inputRelationMetadataKey] = relation
	in.Metadata = metadata
	return in
}

// inputIsTrue treats only an explicit False edge as false. A root stateful
// node or a direct call without the internal marker therefore evaluates True.
func inputIsTrue(in Msg) bool {
	relation, _ := in.Metadata[inputRelationMetadataKey].(string)
	return relation != RelationFalse
}

// stateKeyConfig validates the common per-key settings shared by stateful nodes.
type stateKeyConfig struct {
	KeyFields []string `json:"key_fields"`
	MaxKeys   int      `json:"max_keys"`
}

// normalized applies defaults and rejects invalid bounds and key paths.
func (c stateKeyConfig) normalized() (stateKeyConfig, error) {
	for _, field := range c.KeyFields {
		if err := validateDataPath(field); err != nil {
			return stateKeyConfig{}, fmt.Errorf("key field %q: %w", field, err)
		}
	}
	if c.MaxKeys < 0 {
		return stateKeyConfig{}, fmt.Errorf("max_keys must not be negative")
	}
	if c.MaxKeys == 0 {
		c.MaxKeys = defaultMaxStateKeys
	}
	return c, nil
}

// stateKeyPart distinguishes a missing path from a present JSON null value.
type stateKeyPart struct {
	Present bool `json:"present"`
	Value   any  `json:"value,omitempty"`
}

// resolveStateKey produces an unambiguous JSON key for the configured fields.
// An empty key_fields list intentionally selects one node-wide key.
func resolveStateKey(data map[string]any, fields []string) (string, error) {
	if len(fields) == 0 {
		return "", nil
	}
	parts := make([]stateKeyPart, len(fields))
	for index, field := range fields {
		value, present := resolveDataPath(data, field)
		parts[index] = stateKeyPart{Present: present, Value: value}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("encode state key: %w", err)
	}
	return string(encoded), nil
}

// durationFromMilliseconds validates both positivity and time.Duration range.
func durationFromMilliseconds(value int64, name string) (time.Duration, error) {
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	if value > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("%s exceeds the supported duration", name)
	}
	return time.Duration(value) * time.Millisecond, nil
}

// debounceConfig throttles repeated True evaluations per resolved key.
type debounceConfig struct {
	KeyFields  []string `json:"key_fields"`
	IntervalMS int64    `json:"interval_ms"`
	MaxKeys    int      `json:"max_keys"`
}

// debounceNode emits the first True immediately and later True values only
// after the interval since the last emitted True. False resets the key and is
// always emitted as False. When full, new keys are rejected with False until a
// False evaluation removes an existing key; existing keys remain usable.
type debounceNode struct {
	id       string
	config   debounceConfig
	interval time.Duration
	now      func() time.Time

	mu       sync.Mutex
	lastTrue map[string]time.Time
}

func (n *debounceNode) ID() string   { return n.id }
func (n *debounceNode) Type() string { return NodeTypeDebounce }

// Execute applies event-driven throttling without background goroutines.
func (n *debounceNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	key, err := resolveStateKey(in.Data, n.config.KeyFields)
	if err != nil {
		return nil, fmt.Errorf("debounce key: %w", err)
	}
	if !inputIsTrue(in) {
		n.mu.Lock()
		delete(n.lastTrue, key)
		n.mu.Unlock()
		return []Branch{booleanBranch(in, false)}, nil
	}

	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	last, exists := n.lastTrue[key]
	if exists && now.Sub(last) < n.interval {
		return []Branch{booleanBranch(in, false)}, nil
	}
	if !exists && len(n.lastTrue) >= n.config.MaxKeys {
		return []Branch{booleanBranch(in, false)}, nil
	}
	n.lastTrue[key] = now
	return []Branch{booleanBranch(in, true)}, nil
}

// newDebounceNode validates all settings and initializes bounded local state.
func newDebounceNode(id string, raw json.RawMessage) (Node, error) {
	var config debounceConfig
	if err := decodeClosedConfig(raw, &config); err != nil {
		return nil, err
	}
	keys, err := (stateKeyConfig{KeyFields: config.KeyFields, MaxKeys: config.MaxKeys}).normalized()
	if err != nil {
		return nil, err
	}
	interval, err := durationFromMilliseconds(config.IntervalMS, "interval_ms")
	if err != nil {
		return nil, err
	}
	config.KeyFields = keys.KeyFields
	config.MaxKeys = keys.MaxKeys
	return &debounceNode{
		id: id, config: config, interval: interval, now: time.Now, lastTrue: map[string]time.Time{},
	}, nil
}

// sustainedConfig controls an event-driven continuous-condition check.
type sustainedConfig struct {
	KeyFields  []string `json:"key_fields"`
	DurationMS int64    `json:"duration_ms"`
	MaxKeys    int      `json:"max_keys"`
}

// sustainedState records the uninterrupted True start and one-shot emission.
type sustainedState struct {
	since   time.Time
	emitted bool
}

// sustainedNode emits True once at the first evaluation at or after Duration,
// then suppresses True until a False input resets the key. It never schedules a
// timer. When full, new keys are rejected with False until an existing key is
// reset by False; all state is lost on restart or graph recompilation.
type sustainedNode struct {
	id       string
	config   sustainedConfig
	duration time.Duration
	now      func() time.Time

	mu     sync.Mutex
	states map[string]sustainedState
}

func (n *sustainedNode) ID() string   { return n.id }
func (n *sustainedNode) Type() string { return NodeTypeSustained }

// Execute advances per-key state only when an upstream True/False event arrives.
func (n *sustainedNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	key, err := resolveStateKey(in.Data, n.config.KeyFields)
	if err != nil {
		return nil, fmt.Errorf("sustained key: %w", err)
	}
	if !inputIsTrue(in) {
		n.mu.Lock()
		delete(n.states, key)
		n.mu.Unlock()
		return []Branch{booleanBranch(in, false)}, nil
	}

	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	state, exists := n.states[key]
	if !exists {
		if len(n.states) >= n.config.MaxKeys {
			return []Branch{booleanBranch(in, false)}, nil
		}
		n.states[key] = sustainedState{since: now}
		return []Branch{booleanBranch(in, false)}, nil
	}
	if state.emitted || now.Sub(state.since) < n.duration {
		return []Branch{booleanBranch(in, false)}, nil
	}
	state.emitted = true
	n.states[key] = state
	return []Branch{booleanBranch(in, true)}, nil
}

// newSustainedNode validates all settings and initializes bounded local state.
func newSustainedNode(id string, raw json.RawMessage) (Node, error) {
	var config sustainedConfig
	if err := decodeClosedConfig(raw, &config); err != nil {
		return nil, err
	}
	keys, err := (stateKeyConfig{KeyFields: config.KeyFields, MaxKeys: config.MaxKeys}).normalized()
	if err != nil {
		return nil, err
	}
	duration, err := durationFromMilliseconds(config.DurationMS, "duration_ms")
	if err != nil {
		return nil, err
	}
	config.KeyFields = keys.KeyFields
	config.MaxKeys = keys.MaxKeys
	return &sustainedNode{
		id: id, config: config, duration: duration, now: time.Now, states: map[string]sustainedState{},
	}, nil
}
