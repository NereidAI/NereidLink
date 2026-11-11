// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes.go declares the node type identifiers, the side-effect dependency
// bundle and the factory that compiles a stored node into an executor.

package rule

import (
	"encoding/json"
	"fmt"
)

// Node type identifiers stored in the type column and accepted in graph
// documents. They are wire values, defined once.
const (
	// NodeTypeScriptFilter is the Script Filter node.
	NodeTypeScriptFilter = "script-filter"

	// NodeTypeDeclarativeFilter is the zero-script typed condition filter.
	NodeTypeDeclarativeFilter = "declarative-filter"

	// NodeTypeTimeWindow is the local-time inclusion filter.
	NodeTypeTimeWindow = "time-window"

	// NodeTypeDebounce is the per-key True-event throttle.
	NodeTypeDebounce = "debounce"

	// NodeTypeSustained is the event-driven sustained-condition filter.
	NodeTypeSustained = "sustained"

	// NodeTypeSwitch is the Switch node.
	NodeTypeSwitch = "switch"

	// NodeTypeTransform is the Transform Script node.
	NodeTypeTransform = "transform"

	// NodeTypeSaveAttributes is the Save Attributes node.
	NodeTypeSaveAttributes = "save-attributes"

	// NodeTypeCreateAlarm is the Create Alarm node.
	NodeTypeCreateAlarm = "create-alarm"

	// NodeTypeClearAlarm is the Clear Alarm node.
	NodeTypeClearAlarm = "clear-alarm"

	// NodeTypeSendCommand is the Send Command node.
	NodeTypeSendCommand = "send-command"

	// NodeTypeLog is the Log node.
	NodeTypeLog = "log"

	// NodeTypeDelimiter is the Delimiter node.
	NodeTypeDelimiter = "delimiter"

	// NodeTypeForward is the Forward node.
	NodeTypeForward = "forward"
)

// Deps holds the side-effect dependencies injected into nodes.
type Deps struct {
	// Script evaluates user JavaScript.
	Script Scripter

	// Attributes persists attributes.
	Attributes AttributeSaver

	// Alarms raises and clears alarms.
	Alarms AlarmRaiser

	// Commands dispatches commands.
	Commands CommandDispatcher

	// Egress publishes forwarded messages.
	Egress EgressPublisher
}

// buildNode constructs one executable node from its type and raw config. The
// script-control wrappers carry their branch labels to stateful nodes without
// changing the established Node.Execute signature; declarative nodes tag their
// own boolean outputs.
func buildNode(id, nodeType string, raw json.RawMessage, deps Deps) (node Node, err error) {
	defer func() {
		switch node.(type) {
		case *scriptFilter, *switchNode:
			node = &relationMetadataNode{Node: node}
		}
	}()

	switch nodeType {
	case NodeTypeScriptFilter:
		var config filterConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &scriptFilter{id: id, config: config, script: deps.Script}, nil

	case NodeTypeDeclarativeFilter:
		return newDeclarativeFilterNode(id, raw)

	case NodeTypeTimeWindow:
		return newTimeWindowNode(id, raw)

	case NodeTypeDebounce:
		return newDebounceNode(id, raw)

	case NodeTypeSustained:
		return newSustainedNode(id, raw)

	case NodeTypeSwitch:
		var config switchConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &switchNode{id: id, config: config, script: deps.Script}, nil

	case NodeTypeTransform:
		var config transformConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &transformNode{id: id, config: config, script: deps.Script}, nil

	case NodeTypeSaveAttributes:
		var config saveAttributesConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &saveAttributesNode{
			id: id, config: config, script: deps.Script, saver: deps.Attributes,
		}, nil

	case NodeTypeCreateAlarm:
		var config createAlarmConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &createAlarmNode{
			id: id, config: config, script: deps.Script, alarms: deps.Alarms,
		}, nil

	case NodeTypeClearAlarm:
		var config clearAlarmConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &clearAlarmNode{
			id: id, config: config, script: deps.Script, alarms: deps.Alarms,
		}, nil

	case NodeTypeSendCommand:
		var config sendCommandConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		if err := config.validate(); err != nil {
			return nil, fmt.Errorf("send command config: %w", err)
		}
		return &sendCommandNode{
			id: id, config: config, script: deps.Script, commands: deps.Commands,
		}, nil

	case NodeTypeForward:
		var config forwardConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		if err := config.validate(); err != nil {
			return nil, fmt.Errorf("forward config: %w", err)
		}
		return &forwardNode{id: id, config: config, publisher: deps.Egress}, nil

	case NodeTypeLog:
		var config logConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &logNode{id: id, config: config}, nil

	case NodeTypeDelimiter:
		var config delimiterConfig
		if err := decodeConfig(raw, &config); err != nil {
			return nil, err
		}
		return &delimiterNode{id: id, config: config}, nil

	default:
		return nil, fmt.Errorf("unknown node type: %s", nodeType)
	}
}

// decodeConfig unmarshals node config, tolerating an empty document.
func decodeConfig(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode node config: %w", err)
	}
	return nil
}
