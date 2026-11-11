// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_declarative.go implements closed-vocabulary filtering and local-time
// windows. Neither node evaluates source code or accepts expression syntax.

package rule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// declarativeCondition is one typed comparison against a message data path.
type declarativeCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// declarativeFilterConfig combines All and Any with AND. Every All condition
// must match; a nonempty Any array additionally requires at least one match.
// Empty arrays impose no requirement, so an entirely empty filter emits True.
type declarativeFilterConfig struct {
	All []declarativeCondition `json:"all"`
	Any []declarativeCondition `json:"any"`
}

// declarativeFilterNode evaluates only the declared condition vocabulary.
type declarativeFilterNode struct {
	id     string
	config declarativeFilterConfig
}

func (n *declarativeFilterNode) ID() string   { return n.id }
func (n *declarativeFilterNode) Type() string { return NodeTypeDeclarativeFilter }

// Execute routes to True when the configured conjunction matches and to False
// for mismatches, missing paths, and type-incompatible comparisons.
func (n *declarativeFilterNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	matched := true
	for _, condition := range n.config.All {
		if !evaluateDeclarativeCondition(in.Data, condition) {
			matched = false
			break
		}
	}
	if matched && len(n.config.Any) > 0 {
		matched = false
		for _, condition := range n.config.Any {
			if evaluateDeclarativeCondition(in.Data, condition) {
				matched = true
				break
			}
		}
	}
	return []Branch{booleanBranch(in, matched)}, nil
}

// newDeclarativeFilterNode decodes and validates the complete condition
// vocabulary before a graph can execute the node.
func newDeclarativeFilterNode(id string, raw json.RawMessage) (Node, error) {
	var config declarativeFilterConfig
	if err := decodeClosedConfig(raw, &config); err != nil {
		return nil, err
	}
	for index, condition := range append(append([]declarativeCondition{}, config.All...), config.Any...) {
		if err := validateDataPath(condition.Field); err != nil {
			return nil, fmt.Errorf("condition %d field: %w", index, err)
		}
		switch condition.Op {
		case "eq", "ne", "gt", "gte", "lt", "lte", "contains":
			if condition.Value == nil {
				return nil, fmt.Errorf("condition %d operator %q requires a non-null value", index, condition.Op)
			}
		case "exists":
		default:
			return nil, fmt.Errorf("condition %d has unknown operator %q", index, condition.Op)
		}
	}
	return &declarativeFilterNode{id: id, config: config}, nil
}

// evaluateDeclarativeCondition deliberately rejects cross-type comparisons;
// in particular, ne does not turn an invalid comparison into a match.
func evaluateDeclarativeCondition(data map[string]any, condition declarativeCondition) bool {
	actual, exists := resolveDataPath(data, condition.Field)
	if condition.Op == "exists" {
		return exists
	}
	if !exists {
		return false
	}

	switch condition.Op {
	case "eq":
		equal, valid := equalJSONScalar(actual, condition.Value)
		return valid && equal
	case "ne":
		equal, valid := equalJSONScalar(actual, condition.Value)
		return valid && !equal
	case "gt", "gte", "lt", "lte":
		comparison, valid := compareJSONOrdered(actual, condition.Value)
		if !valid {
			return false
		}
		switch condition.Op {
		case "gt":
			return comparison > 0
		case "gte":
			return comparison >= 0
		case "lt":
			return comparison < 0
		default:
			return comparison <= 0
		}
	case "contains":
		return containsJSONValue(actual, condition.Value)
	default:
		return false
	}
}

// equalJSONScalar compares the scalar types representable by JSON while
// treating every numeric representation as one JSON number domain.
func equalJSONScalar(left, right any) (bool, bool) {
	if leftNumber, ok := jsonNumber(left); ok {
		rightNumber, valid := jsonNumber(right)
		return valid && leftNumber == rightNumber, valid
	}
	switch left := left.(type) {
	case string:
		right, ok := right.(string)
		return ok && left == right, ok
	case bool:
		right, ok := right.(bool)
		return ok && left == right, ok
	default:
		return false, false
	}
}

// compareJSONOrdered supports numbers and lexicographically ordered strings.
func compareJSONOrdered(left, right any) (int, bool) {
	if leftNumber, ok := jsonNumber(left); ok {
		rightNumber, valid := jsonNumber(right)
		if !valid {
			return 0, false
		}
		switch {
		case leftNumber < rightNumber:
			return -1, true
		case leftNumber > rightNumber:
			return 1, true
		default:
			return 0, true
		}
	}
	leftString, leftOK := left.(string)
	rightString, rightOK := right.(string)
	if !leftOK || !rightOK {
		return 0, false
	}
	return strings.Compare(leftString, rightString), true
}

// containsJSONValue implements substring and JSON-array membership without
// coercing values between strings, booleans, and numbers.
func containsJSONValue(container, expected any) bool {
	switch container := container.(type) {
	case string:
		expectedString, ok := expected.(string)
		return ok && strings.Contains(container, expectedString)
	case []any:
		for _, item := range container {
			if equal, valid := equalJSONScalar(item, expected); valid && equal {
				return true
			}
		}
	case []string:
		expectedString, ok := expected.(string)
		if !ok {
			return false
		}
		for _, item := range container {
			if item == expectedString {
				return true
			}
		}
	}
	return false
}

// jsonNumber accepts the numeric forms used by encoding/json and by tests or
// internal producers constructing Msg values directly. Non-finite values are
// not JSON numbers and therefore never participate in comparisons.
func jsonNumber(value any) (float64, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int8:
		number = float64(value)
	case int16:
		number = float64(value)
	case int32:
		number = float64(value)
	case int64:
		number = float64(value)
	case uint:
		number = float64(value)
	case uint8:
		number = float64(value)
	case uint16:
		number = float64(value)
	case uint32:
		number = float64(value)
	case uint64:
		number = float64(value)
	case json.Number:
		parsed, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// timeWindowConfig is an inclusive-start, exclusive-end local-time window.
// Start equal to End means the full day; Start later than End crosses midnight.
type timeWindowConfig struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// timeWindowNode evaluates timestamps in one validated IANA location.
type timeWindowNode struct {
	id       string
	config   timeWindowConfig
	location *time.Location
	start    int
	end      int
	now      func() time.Time
}

func (n *timeWindowNode) ID() string   { return n.id }
func (n *timeWindowNode) Type() string { return NodeTypeTimeWindow }

// Execute uses the event timestamp when present and processing time otherwise.
func (n *timeWindowNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	at := time.Now()
	if n.now != nil {
		at = n.now()
	}
	if in.Ts > 0 {
		at = time.UnixMilli(in.Ts)
	}
	local := at.In(n.location)
	minute := local.Hour()*60 + local.Minute()
	inside := n.start == n.end || (n.start < n.end && minute >= n.start && minute < n.end) ||
		(n.start > n.end && (minute >= n.start || minute < n.end))
	return []Branch{booleanBranch(in, inside)}, nil
}

// newTimeWindowNode validates clock fields and loads the timezone at build
// time so execution never discovers configuration errors.
func newTimeWindowNode(id string, raw json.RawMessage) (Node, error) {
	config := timeWindowConfig{Timezone: "UTC"}
	if err := decodeClosedConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Timezone == "" {
		config.Timezone = "UTC"
	}
	start, err := parseClockMinutes(config.Start)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	end, err := parseClockMinutes(config.End)
	if err != nil {
		return nil, fmt.Errorf("end: %w", err)
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", config.Timezone, err)
	}
	return &timeWindowNode{
		id: id, config: config, location: location, start: start, end: end, now: time.Now,
	}, nil
}

// parseClockMinutes accepts exactly HH:MM in the 24-hour range.
func parseClockMinutes(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, fmt.Errorf("%q must use HH:MM 24-hour format", value)
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("%q must use HH:MM 24-hour format: %w", value, err)
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

// decodeClosedConfig rejects unknown keys throughout a declarative config and
// rejects trailing JSON documents. An absent document retains zero values for
// the node-specific validator to accept or reject.
func decodeClosedConfig(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode node config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode node config: multiple JSON documents")
		}
		return fmt.Errorf("decode node config: %w", err)
	}
	return nil
}

// validateDataPath rejects ambiguous empty path segments. Paths traverse only
// JSON objects; dots always separate segments rather than naming literal keys.
func validateDataPath(path string) error {
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return fmt.Errorf("path %q contains an empty segment", path)
		}
	}
	return nil
}

// resolveDataPath resolves a dot-separated path without evaluating code.
func resolveDataPath(data map[string]any, path string) (any, bool) {
	if validateDataPath(path) != nil {
		return nil, false
	}
	var current any = data
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
