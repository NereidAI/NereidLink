// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rule_rest.go adapts rule processing and alarm services to the gateway
// debug/snapshot interfaces using JSON marshalled payloads.

package main

import (
	"context"
	"encoding/json"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/gatewayapi"
	"github.com/nereidai/nereidlink/internal/rule"
)

// chainAdminAdapter implements gatewayapi.RuleChainAdmin, invalidating
// the processor cache of the activated group after activation.
type chainAdminAdapter struct {
	store     *rule.ChainStore
	processor *rule.Processor
}

// ListGroups delegates to the chain store.
func (a *chainAdminAdapter) ListGroups(ctx context.Context, tenantID string) ([]rule.GroupMeta, error) {
	return a.store.ListGroups(ctx, tenantID)
}

// CreateGroup delegates to the chain store.
func (a *chainAdminAdapter) CreateGroup(ctx context.Context, tenantID, name string) (string, error) {
	return a.store.CreateGroup(ctx, tenantID, name)
}

// DeleteGroup delegates to the chain store and drops cached graphs of the
// removed group.
func (a *chainAdminAdapter) DeleteGroup(ctx context.Context, tenantID, groupID string) error {
	if err := a.store.DeleteGroup(ctx, tenantID, groupID); err != nil {
		return err
	}
	a.processor.InvalidateGroup(groupID)
	return nil
}

// ListChainVersions delegates to the chain store.
func (a *chainAdminAdapter) ListChainVersions(ctx context.Context, tenantID, groupID string) ([]rule.ChainMeta, error) {
	return a.store.ListChainVersions(ctx, tenantID, groupID)
}

// Graph delegates to the chain store.
func (a *chainAdminAdapter) Graph(ctx context.Context, chainID string) (rule.GraphDTO, error) {
	return a.store.Graph(ctx, chainID)
}

// CreateDraft delegates to the chain store.
func (a *chainAdminAdapter) CreateDraft(ctx context.Context, tenantID, groupID string, dto rule.GraphDTO) (string, rule.GraphDTO, error) {
	return a.store.CreateDraft(ctx, tenantID, groupID, dto)
}

// Activate activates the version then drops cached graphs of its group.
func (a *chainAdminAdapter) Activate(ctx context.Context, tenantID, chainID string) error {
	groupID, err := a.store.GroupOfChain(ctx, tenantID, chainID)
	if err != nil {
		return err
	}
	if err := a.store.Activate(ctx, tenantID, chainID); err != nil {
		return err
	}
	a.processor.InvalidateGroup(groupID)
	return nil
}

// SetGroupEnabled updates the flag and drops cached graphs of its group so
// the suspend/resume takes effect immediately.
func (a *chainAdminAdapter) SetGroupEnabled(ctx context.Context, tenantID, groupID string, enabled bool) error {
	if err := a.store.SetGroupEnabled(ctx, tenantID, groupID, enabled); err != nil {
		return err
	}
	a.processor.InvalidateGroup(groupID)
	return nil
}

// ruleRESTAdapter implements gatewayapi.RuleTools.
type ruleRESTAdapter struct {
	processor *rule.Processor
}

// SetNodeDebug toggles node capture.
func (a *ruleRESTAdapter) SetNodeDebug(nodeID string, enabled bool) {
	a.processor.SetNodeDebug(nodeID, enabled)
}

// NodeCaptures returns marshalled captures.
func (a *ruleRESTAdapter) NodeCaptures(nodeID string) json.RawMessage {
	return mustMarshalRaw(a.processor.NodeCaptures(nodeID))
}

// SetChainTrace resolves the active chain when chainID is empty and toggles.
func (a *ruleRESTAdapter) SetChainTrace(ctx context.Context, tenantID, chainID string, enabled bool) {
	if chainID == "" {
		resolved, err := a.processor.ActiveChainID(ctx, tenantID)
		if err != nil {
			return
		}
		chainID = resolved
	}
	a.processor.SetChainTrace(chainID, enabled)
}

// ChainTraces returns marshalled traces.
func (a *ruleRESTAdapter) ChainTraces(chainID, traceID string) json.RawMessage {
	return mustMarshalRaw(a.processor.ChainTraces(chainID, traceID))
}

// DryRun returns the marshalled non-destructive trace.
func (a *ruleRESTAdapter) DryRun(ctx context.Context, tenantID string, data, metadata map[string]any) (json.RawMessage, error) {
	trace, err := a.processor.DryRun(ctx, tenantID, data, metadata)
	if err != nil {
		return nil, err
	}
	return mustMarshalRaw(trace), nil
}

// DryRunGraph returns the marshalled non-destructive trace for a graph.
func (a *ruleRESTAdapter) DryRunGraph(ctx context.Context, graph rule.GraphDTO, data, metadata map[string]any) (json.RawMessage, error) {
	trace, err := a.processor.DryRunGraph(ctx, graph, data, metadata)
	if err != nil {
		return nil, err
	}
	return mustMarshalRaw(trace), nil
}

// alarmRESTAdapter implements gatewayapi.AlarmAdmin.
type alarmRESTAdapter struct {
	service *alarm.Service
}

// ListPage returns one page of alarm rows matching filter.
func (a *alarmRESTAdapter) ListPage(ctx context.Context, tenantID string, filter alarm.Filter, page, pageSize int) (apipage.Page[alarm.Row], error) {
	return a.service.ListPage(ctx, tenantID, filter, page, pageSize)
}

// Ack acknowledges one active alarm.
func (a *alarmRESTAdapter) Ack(ctx context.Context, tenantID, alarmID string) error {
	return a.service.AckAlarm(ctx, tenantID, alarmID)
}

// Assign assigns one alarm.
func (a *alarmRESTAdapter) Assign(ctx context.Context, tenantID, alarmID, assigneeID string) error {
	return a.service.AssignAlarm(ctx, tenantID, alarmID, assigneeID)
}

// AddComment appends one alarm comment.
func (a *alarmRESTAdapter) AddComment(ctx context.Context, tenantID, alarmID, authorID, body string) (alarm.CommentRow, error) {
	return a.service.AddComment(ctx, tenantID, alarmID, authorID, body)
}

// ListComments returns one alarm's comments.
func (a *alarmRESTAdapter) ListComments(ctx context.Context, tenantID, alarmID string) ([]alarm.CommentRow, error) {
	return a.service.ListComments(ctx, tenantID, alarmID)
}

// mustMarshalRaw encodes trusted internal values to raw JSON.
func mustMarshalRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// Compile-time interface assertions.
var (
	_ gatewayapi.RuleTools      = &ruleRESTAdapter{}
	_ gatewayapi.AlarmAdmin     = &alarmRESTAdapter{}
	_ gatewayapi.RuleChainAdmin = &chainAdminAdapter{}
)
