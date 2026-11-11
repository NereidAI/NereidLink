// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rules.go translates built-in business semantics into the closed declarative
// rule-node vocabulary. It deliberately emits no scripts, expressions, frame
// topology, or stale-reading scheduler that has no executable owner.

package business

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nereidai/nereidlink/internal/rule"
)

func (b *expansionBuilder) expandRuleChains() map[string]string {
	devices := map[string]string{}
	companionsByRule := b.ruleCompanions()
	for _, node := range b.sortedNodes(NodeKindRule) {
		companions := companionsByRule[node.ID]
		deviceID := ""
		depends := []string{}
		for _, companionID := range companions {
			depends = append(depends, b.subscriptionKeys[companionID]...)
			if deviceID == "" {
				deviceID = assetString(b.nodes[companionID], "conclusion_device_id")
			}
		}
		devices[node.ID] = deviceID

		graphBuilder := ruleGraphBuilder{owner: b, ruleNode: node}
		switch b.ruleFamily() {
		case "face-access-control":
			graphBuilder.buildFace(b.parameters)
		case "meter-reading":
			graphBuilder.buildMeter(b.parameters)
		}
		graphBuilder.buildForwarders()

		name := node.Name
		if name == "" {
			name = node.ID
		}
		spec := map[string]any{
			"device_id": deviceID,
			"graph":     graphBuilder.graph(),
			"name":      name,
		}
		if b.ruleFamily() == "meter-reading" {
			spec["declarative"] = map[string]any{
				"stale_after_seconds": parameterInt(b.parameters, "stale_after_seconds", 300),
				"unit":                parameterString(b.parameters, "unit", ""),
			}
		}
		b.artifacts = append(b.artifacts, Artifact{
			Key: ruleChainKey(node.ID), Kind: ArtifactKindRuleChain, DeviceID: deviceID,
			DependsOn: sortedUnique(nonemptyStrings(depends)), Spec: spec,
		})
	}
	b.expandCompanionForwardChains()
	return devices
}

func (b *expansionBuilder) expandCompanionForwardChains() {
	for _, companion := range b.sortedNodes(NodeKindCompanion) {
		if b.ruleConsumingCompanion(companion.ID) != "" {
			continue
		}
		hasNorthbound := false
		for _, edge := range b.edges(NodeKindCompanion, NodeKindNorthbound, RelationPublishes) {
			if edge.Source == companion.ID {
				hasNorthbound = true
				break
			}
		}
		if !hasNorthbound {
			continue
		}
		deviceID := assetString(companion, "conclusion_device_id")
		graphBuilder := ruleGraphBuilder{owner: b, ruleNode: companion}
		graphBuilder.buildCompanionForwarders()
		name := companion.Name
		if name == "" {
			name = companion.ID
		}
		b.artifacts = append(b.artifacts, Artifact{
			Key: companionRuleChainKey(companion.ID), Kind: ArtifactKindRuleChain, DeviceID: deviceID,
			DependsOn: sortedUnique(nonemptyStrings(b.subscriptionKeys[companion.ID])),
			Spec: map[string]any{
				"device_id": deviceID,
				"graph":     graphBuilder.graph(),
				"name":      name,
			},
		})
	}
}

func (b *expansionBuilder) ruleCompanions() map[string][]string {
	pipelineCompanions := map[string][]string{}
	for _, edge := range b.edges(NodeKindPipeline, NodeKindCompanion, RelationFacts) {
		pipelineCompanions[edge.Source] = append(pipelineCompanions[edge.Source], edge.Target)
	}
	sets := map[string]map[string]struct{}{}
	for _, edge := range b.assembly.Edges {
		source, sourceOK := b.nodes[edge.Source]
		target, targetOK := b.nodes[edge.Target]
		if !sourceOK || !targetOK || target.Kind != NodeKindRule {
			continue
		}
		if sets[target.ID] == nil {
			sets[target.ID] = map[string]struct{}{}
		}
		switch {
		case source.Kind == NodeKindCompanion && edge.Relation == RelationConclusions:
			sets[target.ID][source.ID] = struct{}{}
		case source.Kind == NodeKindPipeline && edge.Relation == RelationFacts:
			for _, companionID := range pipelineCompanions[source.ID] {
				sets[target.ID][companionID] = struct{}{}
			}
		}
	}
	result := map[string][]string{}
	for ruleID, set := range sets {
		for companionID := range set {
			result[ruleID] = append(result[ruleID], companionID)
		}
		result[ruleID] = sortedUnique(result[ruleID])
	}
	return result
}

func (b *expansionBuilder) ruleFamily() string {
	if b.template.ID == "face-access-control" || b.template.ID == "meter-reading" {
		return b.template.ID
	}
	family := ""
	for _, pipeline := range b.sortedNodes(NodeKindPipeline) {
		candidate := ""
		switch pipeline.Ref {
		case "face-recognition-v1":
			candidate = "face-access-control"
		case "meter-reader-v1":
			candidate = "meter-reading"
		}
		if candidate != "" && family != "" && candidate != family {
			return ""
		}
		if candidate != "" {
			family = candidate
		}
	}
	return family
}

type ruleGraphBuilder struct {
	owner    *expansionBuilder
	ruleNode Node
	nodes    []rule.NodeDTO
	edges    []rule.EdgeDTO
}

func (g *ruleGraphBuilder) buildFace(parameters map[string]any) {
	matched := g.addNode("matched", rule.NodeTypeDeclarativeFilter, "Matched above threshold", map[string]any{
		"all": []any{
			map[string]any{"field": "matched", "op": "eq", "value": true},
			map[string]any{"field": "score", "op": "gt", "value": parameterFloat(parameters, "threshold", 0.72)},
		},
	})
	working := g.addNode("working-hours", rule.NodeTypeTimeWindow, "Working hours", map[string]any{
		"end":   parameterString(parameters, "working_end", "18:00"),
		"start": parameterString(parameters, "working_start", "08:00"),
	})
	g.addEdge(matched, working, rule.RelationTrue)
	commandSource := working
	if seconds := parameterInt(parameters, "debounce_seconds", 3); seconds > 0 {
		debounced := g.addNode("door-debounce", rule.NodeTypeDebounce, "Door command debounce", map[string]any{
			"interval_ms": seconds * 1000,
			"key_fields":  []string{"camera"},
		})
		g.addEdge(working, debounced, rule.RelationTrue)
		g.addEdge(working, debounced, rule.RelationFalse)
		g.addEdge(matched, debounced, rule.RelationFalse)
		commandSource = debounced
	}
	for _, action := range g.owner.ruleActions(g.ruleNode.ID, "send-command") {
		command := g.addCommand("door:"+action.ID, action, "")
		g.addEdge(commandSource, command, rule.RelationTrue)
	}

	if !parameterBool(parameters, "unknown_alarm", true) {
		return
	}
	unknown := g.addNode("unknown", rule.NodeTypeDeclarativeFilter, "Unknown person", map[string]any{
		"all": []any{
			map[string]any{"field": "matched", "op": "eq", "value": false},
		},
	})
	sustained := g.addNode("unknown-sustained", rule.NodeTypeSustained, "Sustained unknown person", map[string]any{
		"duration_ms": parameterInt(parameters, "unknown_after_seconds", 10) * 1000,
		"key_fields":  []string{"camera"},
	})
	g.addEdge(unknown, sustained, rule.RelationTrue)
	g.addEdge(unknown, sustained, rule.RelationFalse)
	for _, action := range g.owner.ruleActions(g.ruleNode.ID, "create-alarm") {
		alarm := g.addAlarm("unknown:"+action.ID, action)
		g.addEdge(sustained, alarm, rule.RelationTrue)
	}
}

func (g *ruleGraphBuilder) buildMeter(parameters map[string]any) {
	lowConfidence := g.addNode("low-confidence", rule.NodeTypeDeclarativeFilter, "Low confidence", map[string]any{
		"all": []any{
			map[string]any{
				"field": "confidence", "op": "lt",
				"value": parameterFloat(parameters, "low_confidence_threshold", 0.8),
			},
		},
	})
	for _, action := range g.owner.ruleActions(g.ruleNode.ID, "send-command") {
		command := g.addCommand("retry:"+action.ID, action, parameterString(parameters, "retry_command", "Capture"))
		g.addEdge(lowConfidence, command, rule.RelationTrue)
	}

	filters := []struct {
		id       string
		name     string
		operator string
		value    float64
	}{
		{id: "below-minimum", name: "Value below minimum", operator: "lt", value: parameterFloat(parameters, "min_value", 0)},
		{id: "above-maximum", name: "Value above maximum", operator: "gt", value: parameterFloat(parameters, "max_value", 0)},
	}
	alarms := g.owner.ruleActions(g.ruleNode.ID, "create-alarm")
	for _, filter := range filters {
		filterID := g.addNode(filter.id, rule.NodeTypeDeclarativeFilter, filter.name, map[string]any{
			"all": []any{
				map[string]any{"field": "value", "op": filter.operator, "value": filter.value},
			},
		})
		for _, action := range alarms {
			alarmID := g.addAlarm(filter.id+":"+action.ID, action)
			g.addEdge(filterID, alarmID, rule.RelationTrue)
		}
	}
}

func (g *ruleGraphBuilder) buildForwarders() {
	for _, edge := range g.owner.edges(NodeKindRule, NodeKindNorthbound, RelationPublishes) {
		if edge.Source != g.ruleNode.ID {
			continue
		}
		northbound := g.owner.nodes[edge.Target]
		g.addNode("forward:"+northbound.ID, rule.NodeTypeForward, "Forward "+northbound.Name, map[string]any{
			"channel":      assetString(northbound, "channel"),
			"fields":       assetStrings(northbound, "readings"),
			"forwarder_id": assetString(northbound, "forwarder_id"),
		})
	}
	companions := map[string]struct{}{}
	for _, companionID := range g.owner.ruleCompanions()[g.ruleNode.ID] {
		companions[companionID] = struct{}{}
	}
	for _, edge := range g.owner.edges(NodeKindCompanion, NodeKindNorthbound, RelationPublishes) {
		if _, ok := companions[edge.Source]; !ok {
			continue
		}
		northbound := g.owner.nodes[edge.Target]
		g.addNode("forward:companion:"+northbound.ID, rule.NodeTypeForward, "Forward "+northbound.Name, map[string]any{
			"channel":      assetString(northbound, "channel"),
			"fields":       assetStrings(northbound, "readings"),
			"forwarder_id": assetString(northbound, "forwarder_id"),
		})
	}
}

func (g *ruleGraphBuilder) buildCompanionForwarders() {
	for _, edge := range g.owner.edges(NodeKindCompanion, NodeKindNorthbound, RelationPublishes) {
		if edge.Source != g.ruleNode.ID {
			continue
		}
		northbound := g.owner.nodes[edge.Target]
		g.addNode("forward:"+northbound.ID, rule.NodeTypeForward, "Forward "+northbound.Name, map[string]any{
			"channel":      assetString(northbound, "channel"),
			"fields":       assetStrings(northbound, "readings"),
			"forwarder_id": assetString(northbound, "forwarder_id"),
		})
	}
}

func (g *ruleGraphBuilder) addCommand(localID string, action Node, commandOverride string) string {
	commandName := commandOverride
	if commandName == "" {
		commandName = firstNonempty(assetString(action, "command_name"), assetString(action, "resource"))
	}
	params := assetObject(action, "params")
	return g.addNode(localID, rule.NodeTypeSendCommand, action.Name, map[string]any{
		"command_name":     commandName,
		"params":           params,
		"target_device_id": assetString(action, "target_device_id"),
		"timeout_ms":       assetInt(action, "timeout_ms", 0),
	})
}

func (g *ruleGraphBuilder) addAlarm(localID string, action Node) string {
	config := map[string]any{
		"alarm_type": assetString(action, "alarm_type"),
		"propagate":  assetBool(action, "propagate", false),
		"severity":   assetString(action, "severity"),
	}
	if chainID := assetString(action, "escalation_chain_id"); chainID != "" {
		config["escalation_chain_id"] = chainID
	}
	return g.addNode(localID, rule.NodeTypeCreateAlarm, action.Name, config)
}

func (g *ruleGraphBuilder) addNode(localID, nodeType, name string, config map[string]any) string {
	raw, err := json.Marshal(config)
	if err != nil {
		g.owner.addIssue("encoding_failed", fmt.Sprintf("encode declarative rule config: %v", err), g.ruleNode.ID, ruleChainKey(g.ruleNode.ID))
		raw = json.RawMessage("{}")
	}
	id := g.ruleNode.ID + ":" + localID
	index := len(g.nodes)
	x := float64((index % 4) * 240)
	y := float64((index / 4) * 160)
	g.nodes = append(g.nodes, rule.NodeDTO{
		ID: id, Type: nodeType, Name: name, Config: raw, PositionX: &x, PositionY: &y,
	})
	return id
}

func (g *ruleGraphBuilder) addEdge(from, to, relation string) {
	g.edges = append(g.edges, rule.EdgeDTO{From: from, To: to, Relation: relation})
}

func (g *ruleGraphBuilder) graph() rule.GraphDTO {
	nodes := append([]rule.NodeDTO(nil), g.nodes...)
	edges := append([]rule.EdgeDTO(nil), g.edges...)
	if nodes == nil {
		nodes = []rule.NodeDTO{}
	}
	if edges == nil {
		edges = []rule.EdgeDTO{}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relation < edges[j].Relation
	})
	return rule.GraphDTO{Nodes: nodes, Edges: edges}
}

func (b *expansionBuilder) ruleActions(ruleID, actionType string) []Node {
	actions := []Node{}
	for _, edge := range b.edges(NodeKindRule, NodeKindAction, RelationTriggers) {
		if edge.Source != ruleID {
			continue
		}
		action := b.nodes[edge.Target]
		if actionTypeOf(action) == actionType {
			actions = append(actions, action)
		}
	}
	return actions
}

func actionTypeOf(action Node) string {
	switch assetString(action, "action_type") {
	case "command", rule.NodeTypeSendCommand:
		return rule.NodeTypeSendCommand
	case "alarm", rule.NodeTypeCreateAlarm:
		return rule.NodeTypeCreateAlarm
	default:
		return ""
	}
}
