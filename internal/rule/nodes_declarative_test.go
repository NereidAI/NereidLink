// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_declarative_test.go verifies typed operators, path handling, closed
// configuration decoding, overnight windows, timezones, and timestamp fallback.

package rule

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// executeRelation executes one node and asserts its single output relation.
func executeRelation(t *testing.T, node Node, in Msg) string {
	t.Helper()
	branches, err := node.Execute(context.Background(), in, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(branches) != 1 {
		t.Fatalf("branches = %d, want 1", len(branches))
	}
	return branches[0].Relation
}

// TestDeclarativeFilterOperators covers every operator and invalid type paths.
func TestDeclarativeFilterOperators(t *testing.T) {
	tests := []struct {
		name  string
		op    string
		value any
		data  map[string]any
		want  string
	}{
		{name: "equal JSON numbers", op: "eq", value: float64(10), data: map[string]any{"value": 10}, want: RelationTrue},
		{name: "unequal numbers", op: "eq", value: 10, data: map[string]any{"value": 11}, want: RelationFalse},
		{name: "equal strings", op: "eq", value: "active", data: map[string]any{"value": "active"}, want: RelationTrue},
		{name: "equal booleans", op: "eq", value: true, data: map[string]any{"value": true}, want: RelationTrue},
		{name: "not equal", op: "ne", value: "idle", data: map[string]any{"value": "active"}, want: RelationTrue},
		{name: "not equal mismatch is invalid", op: "ne", value: "10", data: map[string]any{"value": 10}, want: RelationFalse},
		{name: "greater number", op: "gt", value: 9, data: map[string]any{"value": 10}, want: RelationTrue},
		{name: "greater or equal boundary", op: "gte", value: 10, data: map[string]any{"value": 10}, want: RelationTrue},
		{name: "less string", op: "lt", value: "b", data: map[string]any{"value": "a"}, want: RelationTrue},
		{name: "less or equal string", op: "lte", value: "a", data: map[string]any{"value": "a"}, want: RelationTrue},
		{name: "ordered mismatch is invalid", op: "gt", value: 1, data: map[string]any{"value": "2"}, want: RelationFalse},
		{name: "substring", op: "contains", value: "middle", data: map[string]any{"value": "a middle value"}, want: RelationTrue},
		{name: "substring type mismatch", op: "contains", value: 2, data: map[string]any{"value": "123"}, want: RelationFalse},
		{name: "array number membership", op: "contains", value: 2, data: map[string]any{"value": []any{1, float64(2)}}, want: RelationTrue},
		{name: "array boolean membership", op: "contains", value: true, data: map[string]any{"value": []any{false, true}}, want: RelationTrue},
		{name: "string array membership", op: "contains", value: "b", data: map[string]any{"value": []string{"a", "b"}}, want: RelationTrue},
		{name: "array membership mismatch", op: "contains", value: "2", data: map[string]any{"value": []any{2}}, want: RelationFalse},
		{name: "exists", op: "exists", value: nil, data: map[string]any{"value": false}, want: RelationTrue},
		{name: "exists null", op: "exists", value: nil, data: map[string]any{"value": nil}, want: RelationTrue},
		{name: "exists missing", op: "exists", value: nil, data: map[string]any{}, want: RelationFalse},
		{name: "comparison missing", op: "ne", value: 1, data: map[string]any{}, want: RelationFalse},
		{name: "null comparison invalid", op: "eq", value: nil, data: map[string]any{"value": nil}, want: RelationFalse},
		{name: "object comparison invalid", op: "eq", value: map[string]any{}, data: map[string]any{"value": map[string]any{}}, want: RelationFalse},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := &declarativeFilterNode{config: declarativeFilterConfig{
				All: []declarativeCondition{{Field: "value", Op: test.op, Value: test.value}},
			}}
			if got := executeRelation(t, node, Msg{Data: test.data}); got != test.want {
				t.Fatalf("relation = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDeclarativeFilterCombinations verifies all/any and empty-array semantics.
func TestDeclarativeFilterCombinations(t *testing.T) {
	node := &declarativeFilterNode{config: declarativeFilterConfig{
		All: []declarativeCondition{
			{Field: "temperature", Op: "gte", Value: 20},
			{Field: "location.zone", Op: "eq", Value: "north"},
		},
		Any: []declarativeCondition{
			{Field: "active", Op: "eq", Value: true},
			{Field: "override", Op: "eq", Value: true},
		},
	}}
	base := func() map[string]any {
		return map[string]any{
			"temperature": 21,
			"location":    map[string]any{"zone": "north"},
			"active":      false,
			"override":    true,
		}
	}
	if got := executeRelation(t, node, Msg{Data: base()}); got != RelationTrue {
		t.Fatalf("matching relation = %q", got)
	}
	data := base()
	data["override"] = false
	if got := executeRelation(t, node, Msg{Data: data}); got != RelationFalse {
		t.Fatalf("failed any relation = %q", got)
	}
	data = base()
	data["location"] = map[string]any{"zone": "south"}
	if got := executeRelation(t, node, Msg{Data: data}); got != RelationFalse {
		t.Fatalf("failed all relation = %q", got)
	}
	if got := executeRelation(t, &declarativeFilterNode{}, Msg{Data: map[string]any{}}); got != RelationTrue {
		t.Fatalf("empty filter relation = %q, want True", got)
	}
}

// TestDeclarativeFilterBuildValidation rejects unknown vocabulary and malformed paths.
func TestDeclarativeFilterBuildValidation(t *testing.T) {
	valid := `{"all":[{"field":"nested.value","op":"exists"}],"any":[]}`
	if _, err := buildNode("filter", NodeTypeDeclarativeFilter, json.RawMessage(valid), Deps{}); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	invalid := []string{
		`{"unknown":true}`,
		`{"all":[{"field":"value","op":"eq","value":1,"unknown":true}]}`,
		`{"all":[{"field":"value","op":"javascript","value":1}]}`,
		`{"all":[{"field":"","op":"exists"}]}`,
		`{"all":[{"field":"a..b","op":"exists"}]}`,
		`{"all":[{"field":"value","op":"eq"}]}`,
		`{"all":[{"field":"value","op":"eq","value":null}]}`,
		`{} {}`,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := buildNode("filter", NodeTypeDeclarativeFilter, json.RawMessage(raw), Deps{}); err == nil {
				t.Fatal("expected build error")
			}
		})
	}
}

// TestTimeWindowOvernight verifies inclusive start, exclusive end, and midnight crossing.
func TestTimeWindowOvernight(t *testing.T) {
	built, err := newTimeWindowNode("window", json.RawMessage(`{"start":"22:00","end":"06:00"}`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	node := built.(*timeWindowNode)
	tests := []struct {
		clock string
		want  string
	}{
		{clock: "21:59", want: RelationFalse},
		{clock: "22:00", want: RelationTrue},
		{clock: "23:30", want: RelationTrue},
		{clock: "00:00", want: RelationTrue},
		{clock: "05:59", want: RelationTrue},
		{clock: "06:00", want: RelationFalse},
		{clock: "12:00", want: RelationFalse},
	}
	for _, test := range tests {
		t.Run(test.clock, func(t *testing.T) {
			at, err := time.ParseInLocation("2006-01-02 15:04", "2026-01-15 "+test.clock, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if got := executeRelation(t, node, Msg{Ts: at.UnixMilli()}); got != test.want {
				t.Fatalf("relation = %q, want %q", got, test.want)
			}
		})
	}
}

// TestTimeWindowTimezoneAndFallback verifies IANA conversion and processing-time fallback.
func TestTimeWindowTimezoneAndFallback(t *testing.T) {
	built, err := newTimeWindowNode("window", json.RawMessage(
		`{"start":"09:00","end":"17:00","timezone":"America/New_York"}`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	node := built.(*timeWindowNode)
	insideUTC := time.Date(2026, time.January, 15, 14, 0, 0, 0, time.UTC)
	outsideUTC := time.Date(2026, time.January, 15, 13, 59, 0, 0, time.UTC)
	if got := executeRelation(t, node, Msg{Ts: insideUTC.UnixMilli()}); got != RelationTrue {
		t.Fatalf("09:00 New York relation = %q", got)
	}
	if got := executeRelation(t, node, Msg{Ts: outsideUTC.UnixMilli()}); got != RelationFalse {
		t.Fatalf("08:59 New York relation = %q", got)
	}
	node.now = func() time.Time { return insideUTC }
	if got := executeRelation(t, node, Msg{}); got != RelationTrue {
		t.Fatalf("fallback relation = %q", got)
	}

	allDay, err := newTimeWindowNode("all-day", json.RawMessage(`{"start":"00:00","end":"00:00"}`))
	if err != nil {
		t.Fatalf("build all-day: %v", err)
	}
	if got := executeRelation(t, allDay, Msg{Ts: outsideUTC.UnixMilli()}); got != RelationTrue {
		t.Fatalf("equal-boundary full-day relation = %q", got)
	}
}

// TestTimeWindowBuildValidation rejects malformed clocks, zones, and unknown keys.
func TestTimeWindowBuildValidation(t *testing.T) {
	invalid := []string{
		`{"start":"9:00","end":"17:00"}`,
		`{"start":"24:00","end":"17:00"}`,
		`{"start":"09:00","end":"17:60"}`,
		`{"start":"09:00"}`,
		`{"start":"09:00","end":"17:00","timezone":"Mars/Base"}`,
		`{"start":"09:00","end":"17:00","expression":"now()"}`,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := buildNode("window", NodeTypeTimeWindow, json.RawMessage(raw), Deps{}); err == nil {
				t.Fatal("expected build error")
			}
		})
	}
}
