// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_action.go implements the side-effect nodes: Save Attributes, Create
// Alarm, Clear Alarm and Send Command, plus their dependency interfaces.

package rule

import (
	"context"
	"fmt"
)

// AttributeSaver persists attributes produced by the Save Attributes node.
type AttributeSaver interface {
	// SaveAttributes upserts one or more attributes for a device scope.
	SaveAttributes(ctx context.Context, tenantID, deviceID, scope string, attrs map[string]any) error
}

// AlarmRaiser creates and clears alarms.
type AlarmRaiser interface {
	// CreateAlarm opens or updates the alarm keyed by dedupeKey.
	CreateAlarm(ctx context.Context, input AlarmInput) (AlarmOutcome, error)

	// ClearAlarm clears the live alarm keyed by dedupeKey.
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (AlarmOutcome, error)
}

// AlarmInput is the create-alarm node input.
type AlarmInput struct {
	// TenantID owns the device.
	TenantID string

	// DeviceID is the alarm originator.
	DeviceID string

	// RuleID identifies the node/chain that raised the alarm.
	RuleID string

	// AlarmType classifies the alarm.
	AlarmType string

	// Severity is the alarm severity.
	Severity string

	// DedupeKey identifies the open alarm.
	DedupeKey string

	// Details is the alarm detail document.
	Details map[string]any

	// Propagate marks propagation up the relation hierarchy.
	Propagate bool

	// EscalationChainID binds the escalation chain applied to this alarm.
	EscalationChainID string
}

// AlarmOutcome reports the result of an alarm transition.
type AlarmOutcome struct {
	// AlarmID is the alarm identifier.
	AlarmID string

	// Created is true for a new alarm, false for an update/clear.
	Created bool

	// Severity is the current severity.
	Severity string

	// Escalated reports a severity increase.
	Escalated bool

	// State is the current lifecycle state.
	State string
}

// CommandDispatcher sends commands for the Send Command node.
type CommandDispatcher interface {
	// DispatchCommand sends one catalog command and returns its status.
	DispatchCommand(ctx context.Context, tenantID, deviceID, name string, params map[string]any, timeoutMS int64) (string, error)
}

// saveAttributesConfig is the Save Attributes node configuration.
type saveAttributesConfig struct {
	// Scope is CLIENT (MVP) or server scopes.
	Scope string `json:"scope"`

	// Script returns the attributes object to persist.
	Script string `json:"script"`
}

// saveAttributesNode persists the script-returned attributes.
type saveAttributesNode struct {
	id     string
	config saveAttributesConfig
	script Scripter
	saver  AttributeSaver
}

func (n *saveAttributesNode) ID() string   { return n.id }
func (n *saveAttributesNode) Type() string { return NodeTypeSaveAttributes }

func (n *saveAttributesNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	attrs, err := n.script.EvalObject(n.config.Script, in.Data, in.Metadata)
	if err != nil {
		return nil, fmt.Errorf("save attributes mapping: %w", err)
	}

	scope := n.config.Scope
	if scope == "" {
		scope = "CLIENT"
	}

	if !dryRun {
		if err := n.saver.SaveAttributes(ctx, in.TenantID, in.DeviceID, scope, attrs); err != nil {
			return nil, fmt.Errorf("save attributes: %w", err)
		}
	}
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}

// createAlarmConfig is the Create Alarm node configuration.
type createAlarmConfig struct {
	// AlarmType classifies the alarm.
	AlarmType string `json:"alarm_type"`

	// Severity is the static severity.
	Severity string `json:"severity"`

	// DedupeKeyScript optionally returns a custom dedupe key.
	DedupeKeyScript string `json:"dedupe_key_script"`

	// DetailsScript optionally returns the alarm details document.
	DetailsScript string `json:"details_script"`

	// Propagate marks propagation.
	Propagate bool `json:"propagate"`

	// EscalationChainID optionally binds an escalation chain.
	EscalationChainID string `json:"escalation_chain_id"`
}

// createAlarmNode opens or updates a deduped alarm.
type createAlarmNode struct {
	id     string
	config createAlarmConfig
	script Scripter
	alarms AlarmRaiser
}

func (n *createAlarmNode) ID() string   { return n.id }
func (n *createAlarmNode) Type() string { return NodeTypeCreateAlarm }

func (n *createAlarmNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	dedupeKey := in.DeviceID + "|" + n.config.AlarmType
	if n.config.DedupeKeyScript != "" {
		result, err := n.script.EvalObject(n.config.DedupeKeyScript, in.Data, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("alarm dedupe script: %w", err)
		}
		if key, ok := result["key"].(string); ok {
			dedupeKey = key
		}
	}

	var details map[string]any
	if n.config.DetailsScript != "" {
		result, err := n.script.EvalObject(n.config.DetailsScript, in.Data, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("alarm details script: %w", err)
		}
		details = result
	}

	if dryRun {
		return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
	}

	outcome, err := n.alarms.CreateAlarm(ctx, AlarmInput{
		TenantID:          in.TenantID,
		DeviceID:          in.DeviceID,
		RuleID:            n.id,
		AlarmType:         n.config.AlarmType,
		Severity:          n.config.Severity,
		DedupeKey:         dedupeKey,
		Details:           details,
		Propagate:         n.config.Propagate,
		EscalationChainID: n.config.EscalationChainID,
	})
	if err != nil {
		return nil, fmt.Errorf("create alarm: %w", err)
	}
	_ = outcome
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}

// clearAlarmConfig is the Clear Alarm node configuration.
type clearAlarmConfig struct {
	// AlarmType identifies the alarm to clear by its default dedupe key.
	AlarmType string `json:"alarm_type"`

	// DedupeKeyScript optionally returns the custom dedupe key.
	DedupeKeyScript string `json:"dedupe_key_script"`
}

// clearAlarmNode clears the matching live alarm.
type clearAlarmNode struct {
	id     string
	config clearAlarmConfig
	script Scripter
	alarms AlarmRaiser
}

func (n *clearAlarmNode) ID() string   { return n.id }
func (n *clearAlarmNode) Type() string { return NodeTypeClearAlarm }

func (n *clearAlarmNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	dedupeKey := in.DeviceID + "|" + n.config.AlarmType
	if n.config.DedupeKeyScript != "" {
		result, err := n.script.EvalObject(n.config.DedupeKeyScript, in.Data, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("clear alarm dedupe script: %w", err)
		}
		if key, ok := result["key"].(string); ok {
			dedupeKey = key
		}
	}

	if dryRun {
		return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
	}

	if _, err := n.alarms.ClearAlarm(ctx, in.TenantID, in.DeviceID, dedupeKey); err != nil {
		return nil, fmt.Errorf("clear alarm: %w", err)
	}
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}

// sendCommandConfig is the Send Command node configuration.
type sendCommandConfig struct {
	// CommandName identifies the catalog command.
	CommandName string `json:"command_name"`

	// TargetDeviceID optionally overrides Msg.DeviceID as the command target.
	TargetDeviceID string `json:"target_device_id"`

	// ParamsScript returns the command parameters.
	ParamsScript string `json:"params_script"`

	// Params supplies static parameters when ParamsScript is empty.
	Params map[string]any `json:"params"`

	// TimeoutMS overrides the catalog timeout.
	TimeoutMS int64 `json:"timeout_ms"`
}

// validate keeps static and script parameter sources mutually exclusive.
func (c sendCommandConfig) validate() error {
	if c.Params != nil && c.ParamsScript != "" {
		return fmt.Errorf("params and params_script must not both be configured")
	}
	return nil
}

// sendCommandNode dispatches a catalog command.
type sendCommandNode struct {
	id       string
	config   sendCommandConfig
	script   Scripter
	commands CommandDispatcher
}

func (n *sendCommandNode) ID() string   { return n.id }
func (n *sendCommandNode) Type() string { return NodeTypeSendCommand }

func (n *sendCommandNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	params := n.config.Params
	if params == nil {
		params = map[string]any{}
	}
	if n.config.ParamsScript != "" {
		result, err := n.script.EvalObject(n.config.ParamsScript, in.Data, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("command params script: %w", err)
		}
		params = result
	}

	if dryRun {
		return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
	}

	targetDeviceID := n.config.TargetDeviceID
	if targetDeviceID == "" {
		targetDeviceID = in.DeviceID
	}
	status, err := n.commands.DispatchCommand(ctx,
		in.TenantID, targetDeviceID, n.config.CommandName, params, n.config.TimeoutMS)
	if err != nil {
		return nil, fmt.Errorf("send command: %w", err)
	}
	if status == "FAILED" || status == "TIMEOUT" || status == "NOT_CONNECTED" || status == "EXPIRED" {
		return []Branch{{Relation: RelationFailure, Msg: in}}, nil
	}
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}
