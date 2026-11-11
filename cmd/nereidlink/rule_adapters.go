// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rule_adapters.go bridges the rule package's side-effect interfaces to the
// alarm and command packages.

package main

import (
	"context"
	"encoding/json"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/rule"
)

// alarmAdapter adapts alarm.Raiser to rule.AlarmRaiser.
type alarmAdapter struct {
	raiser alarm.Raiser
}

// CreateAlarm maps a rule alarm input to the alarm package type.
func (a *alarmAdapter) CreateAlarm(ctx context.Context, in rule.AlarmInput) (rule.AlarmOutcome, error) {
	outcome, err := a.raiser.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:          in.TenantID,
		DeviceID:          in.DeviceID,
		RuleID:            in.RuleID,
		AlarmType:         in.AlarmType,
		Severity:          in.Severity,
		DedupeKey:         in.DedupeKey,
		Details:           in.Details,
		Propagate:         in.Propagate,
		EscalationChainID: in.EscalationChainID,
	})
	if err != nil {
		return rule.AlarmOutcome{}, err
	}
	return mapAlarmOutcome(outcome), nil
}

// ClearAlarm maps a rule clear request.
func (a *alarmAdapter) ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (rule.AlarmOutcome, error) {
	outcome, err := a.raiser.ClearAlarm(ctx, tenantID, deviceID, dedupeKey)
	if err != nil {
		return rule.AlarmOutcome{}, err
	}
	return mapAlarmOutcome(outcome), nil
}

// mapAlarmOutcome converts an alarm outcome to the rule package type.
func mapAlarmOutcome(outcome alarm.Outcome) rule.AlarmOutcome {
	return rule.AlarmOutcome{
		AlarmID:   outcome.AlarmID,
		Created:   outcome.Created,
		Severity:  outcome.Severity,
		Escalated: outcome.Escalated,
		State:     outcome.State,
	}
}

// commandAdapter adapts *command.API to rule.CommandDispatcher.
type commandAdapter struct {
	api *command.API
}

// DispatchCommand encodes map parameters and sends the catalog command.
func (a *commandAdapter) DispatchCommand(ctx context.Context, tenantID, deviceID, name string, params map[string]any, timeoutMS int64) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	// DispatchCommand encodes map parameters and sends the catalog command.
	// Rules do not yet choose a direction; the catalog mask/default decides.
	ack, err := a.api.Send(ctx, tenantID, deviceID, name, "", raw, timeoutMS)
	if err != nil {
		return "", err
	}
	return ack.Status, nil
}

// Compile-time interface assertions.
var (
	_ rule.AlarmRaiser       = &alarmAdapter{}
	_ rule.CommandDispatcher = &commandAdapter{}
)
