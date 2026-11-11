// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// expand.go owns deterministic business expansion. It performs no I/O and
// returns partial declarative artifacts with blocking findings instead of
// hiding document errors behind a panic or an all-or-nothing expansion error.

package business

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Expand validates one instance against its immutable template and produces a
// deterministic declarative expansion. The returned report can be invalid even
// when partial artifacts are available for inspection.
func Expand(template Template, input ExpansionInput) (Expansion, ValidationReport) {
	assembly := normalizedAssembly(input.Assembly)
	builder := expansionBuilder{
		template: template,
		input:    input,
		assembly: assembly,
		nodes:    map[string]Node{},
		issues:   make([]Issue, 0),
	}
	builder.parameters = builder.validateParameters()
	builder.validateAssembly()
	builder.expandArtifacts()

	expansion := Expansion{
		SchemaVersion: ExpansionSchemaVersion,
		InstanceID:    input.InstanceID,
		Generation:    input.Generation,
		Template:      TemplateRef{ID: template.ID, Version: template.Version},
		Artifacts:     builder.artifacts,
	}
	if expansion.Artifacts == nil {
		expansion.Artifacts = make([]Artifact, 0)
	}
	digest, err := expansionDigest(expansion)
	if err != nil {
		builder.addIssue("encoding_failed", fmt.Sprintf("encode expansion digest: %v", err), "", "")
	} else {
		expansion.Digest = digest
	}
	builder.sortIssues()
	return expansion, ValidationReport{Valid: !builder.hasBlockingIssue(), Issues: builder.issues}
}

type expansionBuilder struct {
	template   Template
	input      ExpansionInput
	assembly   Assembly
	parameters map[string]any
	nodes      map[string]Node
	issues     []Issue
	artifacts  []Artifact

	ruleDevices      map[string]string
	subscriptionKeys map[string][]string
}

func (b *expansionBuilder) expandArtifacts() {
	targets := b.pipelineTargets()
	pipelineKeys := map[string]map[string]string{}
	modelKeys := map[string]map[string][]string{}
	externalKeys := map[string]map[string][]string{}

	for pipelineID, pipelineTargets := range targets {
		pipelineKeys[pipelineID] = map[string]string{}
		for _, target := range pipelineTargets {
			pipelineKeys[pipelineID][target.gatewayID] = pipelineRegistrationKey(pipelineID, target.gatewayID)
		}
	}

	// Model readiness is declared once for every model node and target gateway.
	// NereidLink never moves model bytes or unloads a servable.
	emittedModels := map[string]struct{}{}
	for _, edge := range b.edges(NodeKindModel, NodeKindPipeline, RelationUses) {
		model := b.nodes[edge.Source]
		for _, target := range targets[edge.Target] {
			key := modelDistributionKey(model.ID, target.gatewayID)
			if _, exists := emittedModels[key]; !exists {
				spec := map[string]any{
					"ensure":   true,
					"model_id": assetString(model, "model_id"),
					"version":  assetString(model, "version"),
				}
				// Optional NH envelope overrides are emitted only when the
				// assembly provides them, so expansions without the fields
				// keep their historical digest. Empty values make the
				// horizon owner apply its defaults (fp16, the neutral
				// inference-model object type, a deterministic object id).
				for _, field := range []string{"precision", "object_type", "object_id"} {
					if value := assetString(model, field); value != "" {
						spec[field] = value
					}
				}
				b.artifacts = append(b.artifacts, Artifact{
					Key: key, Kind: ArtifactKindModelDistribution, GatewayID: target.gatewayID,
					DependsOn: []string{},
					Spec:      spec,
				})
				emittedModels[key] = struct{}{}
			}
			if modelKeys[edge.Target] == nil {
				modelKeys[edge.Target] = map[string][]string{}
			}
			modelKeys[edge.Target][target.gatewayID] = append(modelKeys[edge.Target][target.gatewayID], key)
		}
	}

	for pipelineID, pipelineTargets := range targets {
		pipeline := b.nodes[pipelineID]
		for _, target := range pipelineTargets {
			config := map[string]any{
				"manager_device": "nereid-inference",
				"pipeline_id":    assetString(pipeline, "pipeline_id"),
				"resource":       "PipelinesJSON",
			}
			if definition, ok := pipeline.Asset["definition"]; ok {
				config["definition"] = definition
			}
			depends := append([]string(nil), modelKeys[pipelineID][target.gatewayID]...)
			b.artifacts = append(b.artifacts, Artifact{
				Key: pipelineKeys[pipelineID][target.gatewayID], Kind: ArtifactKindPipelineRegistration,
				GatewayID: target.gatewayID, DependsOn: sortedUnique(depends), Spec: managerResourceSpec(config),
			})
		}
	}

	emittedExternals := map[string]struct{}{}
	for _, edge := range b.edges(NodeKindExternalServable, NodeKindPipeline, RelationServes) {
		external := b.nodes[edge.Source]
		for _, target := range targets[edge.Target] {
			key := externalServableKey(external.ID, target.gatewayID)
			if _, exists := emittedExternals[key]; !exists {
				config := map[string]any{
					"manager_device": "nereid-inference",
					"resource":       "ExternalServablesJSON",
					"entry_id":       assetString(external, "servable_id"),
					"id_field":       "servableId",
				}
				if definition, ok := external.Asset["definition"]; ok {
					config["definition"] = definition
				}
				b.artifacts = append(b.artifacts, Artifact{
					Key: key, Kind: ArtifactKindExternalServable, GatewayID: target.gatewayID,
					DependsOn: []string{}, Spec: managerResourceSpec(config),
				})
				emittedExternals[key] = struct{}{}
			}
			if externalKeys[edge.Target] == nil {
				externalKeys[edge.Target] = map[string][]string{}
			}
			externalKeys[edge.Target][target.gatewayID] = append(externalKeys[edge.Target][target.gatewayID], key)
		}
	}

	b.expandBindings(pipelineKeys, modelKeys, externalKeys)
	b.subscriptionKeys = b.expandSubscriptions(pipelineKeys, targets)
	b.ruleDevices = b.expandRuleChains()
	b.expandNorthboundChannels()

	sort.Slice(b.artifacts, func(i, j int) bool { return b.artifacts[i].Key < b.artifacts[j].Key })
	for index := range b.artifacts {
		b.artifacts[index].DependsOn = sortedUnique(b.artifacts[index].DependsOn)
		if b.artifacts[index].Spec == nil {
			b.artifacts[index].Spec = map[string]any{}
		}
	}
}

func (b *expansionBuilder) expandBindings(
	pipelineKeys map[string]map[string]string,
	modelKeys map[string]map[string][]string,
	externalKeys map[string]map[string][]string,
) {
	companionPipelines := map[string]bool{}
	for _, factEdge := range b.edges(NodeKindPipeline, NodeKindCompanion, RelationFacts) {
		companionPipelines[factEdge.Source] = true
	}
	externals := b.incomingByTarget(NodeKindExternalServable, NodeKindPipeline, RelationServes)
	for _, edge := range b.edges(NodeKindCamera, NodeKindPipeline, RelationFrames) {
		camera := b.nodes[edge.Source]
		gatewayID := assetString(camera, "gateway_id")
		key := inferenceBindingKey(camera.ID, edge.Target)
		publishMode := assetString(camera, "publish_mode")
		if companionPipelines[edge.Target] && publishMode != "" {
			b.addIssue("companion_publish_mode",
				"PublishMode must be empty when a companion consumes pipeline facts",
				camera.ID, key)
		}

		depends := []string{pipelineKeys[edge.Target][gatewayID]}
		depends = append(depends, modelKeys[edge.Target][gatewayID]...)
		depends = append(depends, externalKeys[edge.Target][gatewayID]...)
		config := map[string]any{
			"FPS":             assetFloat(camera, "fps", 0),
			"PipelineId":      assetString(b.nodes[edge.Target], "pipeline_id"),
			"PublishMode":     publishMode,
			"PublishReadings": assetStrings(camera, "publish_readings"),
			"SourceDevice":    firstNonempty(assetString(camera, "source_device"), assetString(camera, "device_id")),
		}
		for _, field := range []struct {
			asset  string
			config string
		}{
			{asset: "source_kind", config: "SourceKind"},
			{asset: "source_url", config: "SourceURL"},
			{asset: "secret_name", config: "SecretName"},
			{asset: "stream_profile", config: "StreamProfile"},
			{asset: "decode_accel", config: "DecodeAccel"},
			{asset: "vaapi_device", config: "VAAPIDevice"},
			{asset: "target_device", config: "TargetDevice"},
		} {
			if value := assetString(camera, field.asset); value != "" {
				config[field.config] = value
			}
		}
		if len(externals[edge.Target]) > 0 {
			external := b.nodes[externals[edge.Target][0].Source]
			config["ServableID"] = assetString(external, "servable_id")
			if targetDevice, _ := config["TargetDevice"].(string); targetDevice != "" {
				b.addIssue("external_target_device",
					"TargetDevice must be empty for an external-servable binding",
					camera.ID, key)
			}
		}

		outputDeviceID := assetString(camera, "output_device_id")
		spec := map[string]any{
			"config":         config,
			"connector_type": "edge-inference",
			"type_version":   "1",
		}
		if outputDeviceID != "" {
			spec["device_id"] = outputDeviceID
		}
		b.artifacts = append(b.artifacts, Artifact{
			Key: key, Kind: ArtifactKindInferenceBinding, GatewayID: gatewayID,
			DeviceID: outputDeviceID, DependsOn: sortedUnique(nonemptyStrings(depends)), Spec: spec,
		})
	}
}

func (b *expansionBuilder) expandSubscriptions(
	pipelineKeys map[string]map[string]string,
	targets map[string][]pipelineTarget,
) map[string][]string {
	keysByCompanion := map[string][]string{}
	for _, edge := range b.edges(NodeKindPipeline, NodeKindCompanion, RelationFacts) {
		companion := b.nodes[edge.Target]
		for _, target := range targets[edge.Source] {
			key := companionSubscriptionKey(edge.Source, companion.ID, target.gatewayID)
			keysByCompanion[companion.ID] = append(keysByCompanion[companion.ID], key)
			b.artifacts = append(b.artifacts, Artifact{
				Key: key, Kind: ArtifactKindCompanionSubscription, GatewayID: target.gatewayID,
				DeviceID:  assetString(companion, "conclusion_device_id"),
				DependsOn: nonemptyStrings([]string{pipelineKeys[edge.Source][target.gatewayID]}),
				Spec: map[string]any{
					"companion_id":         assetString(companion, "companion_id"),
					"conclusion_device_id": assetString(companion, "conclusion_device_id"),
					"pipeline_id":          assetString(b.nodes[edge.Source], "pipeline_id"),
					"source":               "PipelineJSON",
					"source_devices":       b.pipelineSourceDevices(edge.Source, target.gatewayID),
				},
			})
		}
	}
	for companionID := range keysByCompanion {
		keysByCompanion[companionID] = sortedUnique(keysByCompanion[companionID])
	}
	return keysByCompanion
}

func (b *expansionBuilder) expandNorthboundChannels() {
	gateways := b.pipelineGatewaySets()
	channelArtifacts := make([]Artifact, 0)
	for _, edge := range b.assembly.Edges {
		source, sourceOK := b.nodes[edge.Source]
		northbound, targetOK := b.nodes[edge.Target]
		if !sourceOK || !targetOK || northbound.Kind != NodeKindNorthbound || edge.Relation != RelationPublishes {
			continue
		}
		if source.Kind != NodeKindRule && source.Kind != NodeKindCompanion {
			continue
		}
		key := northboundChannelKey(source.ID, northbound.ID)
		deviceID := ""
		depends := []string{}
		if source.Kind == NodeKindRule {
			deviceID = b.ruleDevices[source.ID]
			depends = append(depends, ruleChainKey(source.ID))
		} else {
			deviceID = assetString(source, "conclusion_device_id")
			depends = append(depends, b.subscriptionKeys[source.ID]...)
			if ruleID := b.ruleConsumingCompanion(source.ID); ruleID != "" {
				depends = append(depends, ruleChainKey(ruleID))
			} else {
				depends = append(depends, companionRuleChainKey(source.ID))
			}
		}
		gatewayID, gatewayIssue := b.singleNorthboundGateway(source, gateways)
		if gatewayIssue != "" {
			b.addIssue(gatewayIssue, "northbound output must resolve to exactly one gateway", source.ID, key)
		}
		readings := assetStrings(northbound, "readings")
		resourceNames := assetStrings(northbound, "edgex_resource_names")
		spec := map[string]any{
			"channel":              assetString(northbound, "channel"),
			"device_id":            deviceID,
			"edgex_device_name":    assetString(northbound, "edgex_device_name"),
			"edgex_profile_name":   assetString(northbound, "edgex_profile_name"),
			"edgex_resource_names": resourceNames,
			"forwarder_id":         assetString(northbound, "forwarder_id"),
			"readings":             readings,
			"source_node_id":       source.ID,
		}
		if source.Kind == NodeKindRule {
			spec["rule_id"] = source.ID
		}
		artifact := Artifact{
			Key: key, Kind: ArtifactKindNorthboundChannel, GatewayID: gatewayID, DeviceID: deviceID,
			DependsOn: sortedUnique(nonemptyStrings(depends)), Spec: spec,
		}
		channelArtifacts = append(channelArtifacts, artifact)
		b.artifacts = append(b.artifacts, artifact)
	}
	b.expandNorthboundFilters(channelArtifacts)
}

func (b *expansionBuilder) expandNorthboundFilters(channels []Artifact) {
	byGateway := map[string][]Artifact{}
	gatewaySet := map[string]struct{}{}
	for _, channel := range channels {
		if channel.GatewayID == "" {
			continue
		}
		byGateway[channel.GatewayID] = append(byGateway[channel.GatewayID], channel)
		gatewaySet[channel.GatewayID] = struct{}{}
	}
	gateways := make([]string, 0, len(gatewaySet))
	for gatewayID := range gatewaySet {
		gateways = append(gateways, gatewayID)
	}
	sort.Strings(gateways)
	for _, gatewayID := range gateways {
		contributions := byGateway[gatewayID]
		sort.Slice(contributions, func(i, j int) bool { return contributions[i].Key < contributions[j].Key })
		depends := make([]string, 0, len(contributions))
		for _, contribution := range contributions {
			depends = append(depends, contribution.Key)
		}
		config := northboundFilterConfig(contributions)
		spec := map[string]any{
			"connector_type": "edge-northbound-filter",
			"type_version":   "1",
			"device_id":      "",
			"config":         config,
		}
		b.artifacts = append(b.artifacts, Artifact{
			Key: northboundFilterKey(gatewayID), Kind: ArtifactKindNorthboundFilter,
			GatewayID: gatewayID, DeviceID: "", DependsOn: sortedUnique(depends), Spec: spec,
		})
	}
}

func northboundFilterConfig(channels []Artifact) map[string]any {
	sorted := append([]Artifact(nil), channels...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	rules := make([]map[string]any, 0, len(sorted)*2)
	seenRules := map[string]struct{}{}
	for _, contribution := range sorted {
		deviceName := specString(contribution.Spec, "edgex_device_name")
		profileName := specString(contribution.Spec, "edgex_profile_name")
		resourceNames := sortedUnique(specStrings(contribution.Spec, "edgex_resource_names"))
		allow := map[string]any{
			"action":         "allow",
			"device_names":   []string{deviceName},
			"profile_names":  []string{profileName},
			"resource_names": resourceNames,
		}
		deny := map[string]any{
			"action":        "deny",
			"device_names":  []string{deviceName},
			"profile_names": []string{profileName},
		}
		for _, rule := range []map[string]any{allow, deny} {
			raw, _ := json.Marshal(rule)
			identity := string(raw)
			if _, exists := seenRules[identity]; exists {
				continue
			}
			seenRules[identity] = struct{}{}
			rules = append(rules, rule)
		}
	}
	return map[string]any{
		"enabled":        true,
		"default_action": "allow",
		"rules":          rules,
	}
}

func quiescentNorthboundFilterConfig() map[string]any {
	return map[string]any{
		"enabled":        false,
		"default_action": "allow",
		"rules":          []map[string]any{},
	}
}

func (b *expansionBuilder) pipelineGatewaySets() map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for pipelineID, targets := range b.pipelineTargets() {
		out[pipelineID] = map[string]struct{}{}
		for _, target := range targets {
			out[pipelineID][target.gatewayID] = struct{}{}
		}
	}
	return out
}

func (b *expansionBuilder) singleNorthboundGateway(source Node, gateways map[string]map[string]struct{}) (string, string) {
	pipelineIDs := map[string]struct{}{}
	switch source.Kind {
	case NodeKindCompanion:
		for _, edge := range b.edges(NodeKindPipeline, NodeKindCompanion, RelationFacts) {
			if edge.Target == source.ID {
				pipelineIDs[edge.Source] = struct{}{}
			}
		}
	case NodeKindRule:
		for _, edge := range b.edges(NodeKindPipeline, NodeKindRule, RelationFacts) {
			if edge.Target == source.ID {
				pipelineIDs[edge.Source] = struct{}{}
			}
		}
		for _, companionID := range b.ruleCompanions()[source.ID] {
			for _, edge := range b.edges(NodeKindPipeline, NodeKindCompanion, RelationFacts) {
				if edge.Target == companionID {
					pipelineIDs[edge.Source] = struct{}{}
				}
			}
		}
	}
	gatewaySet := map[string]struct{}{}
	for pipelineID := range pipelineIDs {
		for gatewayID := range gateways[pipelineID] {
			gatewaySet[gatewayID] = struct{}{}
		}
	}
	if len(gatewaySet) == 0 {
		return "", "missing_gateway"
	}
	if len(gatewaySet) > 1 {
		return "", "ambiguous_northbound_gateway"
	}
	for gatewayID := range gatewaySet {
		return gatewayID, ""
	}
	return "", "missing_gateway"
}

func (b *expansionBuilder) ruleConsumingCompanion(companionID string) string {
	for _, edge := range b.edges(NodeKindCompanion, NodeKindRule, RelationConclusions) {
		if edge.Source == companionID {
			return edge.Target
		}
	}
	return ""
}

type pipelineTarget struct {
	gatewayID string
}

// pipelineSourceDevices lists the NI job device names of every camera that
// feeds pipelineID on gatewayID through a frames edge. The companion
// contract keys one subscription row per source device, so the generic
// connector fans one companion_subscription artifact out to these rows.
// Device naming matches the inference binding's SourceDevice resolution.
func (b *expansionBuilder) pipelineSourceDevices(pipelineID, gatewayID string) []string {
	seen := map[string]struct{}{}
	devices := make([]string, 0)
	for _, edge := range b.edges(NodeKindCamera, NodeKindPipeline, RelationFrames) {
		if edge.Target != pipelineID {
			continue
		}
		camera := b.nodes[edge.Source]
		if assetString(camera, "gateway_id") != gatewayID {
			continue
		}
		device := firstNonempty(assetString(camera, "source_device"), assetString(camera, "device_id"))
		if device == "" {
			continue
		}
		if _, exists := seen[device]; exists {
			continue
		}
		seen[device] = struct{}{}
		devices = append(devices, device)
	}
	sort.Strings(devices)
	return devices
}

func (b *expansionBuilder) pipelineTargets() map[string][]pipelineTarget {
	sets := map[string]map[string]struct{}{}
	for _, edge := range b.edges(NodeKindCamera, NodeKindPipeline, RelationFrames) {
		if sets[edge.Target] == nil {
			sets[edge.Target] = map[string]struct{}{}
		}
		sets[edge.Target][assetString(b.nodes[edge.Source], "gateway_id")] = struct{}{}
	}
	targets := map[string][]pipelineTarget{}
	for pipelineID, set := range sets {
		for gatewayID := range set {
			targets[pipelineID] = append(targets[pipelineID], pipelineTarget{gatewayID: gatewayID})
		}
		sort.Slice(targets[pipelineID], func(i, j int) bool {
			return targets[pipelineID][i].gatewayID < targets[pipelineID][j].gatewayID
		})
	}
	return targets
}

func (b *expansionBuilder) edges(sourceKind, targetKind, relation string) []Edge {
	edges := make([]Edge, 0)
	for _, edge := range b.assembly.Edges {
		source, sourceOK := b.nodes[edge.Source]
		target, targetOK := b.nodes[edge.Target]
		if sourceOK && targetOK && source.Kind == sourceKind && target.Kind == targetKind && edge.Relation == relation {
			edges = append(edges, edge)
		}
	}
	return edges
}

func (b *expansionBuilder) incomingByTarget(sourceKind, targetKind, relation string) map[string][]Edge {
	incoming := map[string][]Edge{}
	for _, edge := range b.edges(sourceKind, targetKind, relation) {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}
	return incoming
}

func (b *expansionBuilder) addIssue(code, message, nodeID, artifactKey string) {
	b.issues = append(b.issues, Issue{
		Code: code, Message: message, NodeID: nodeID, ArtifactKey: artifactKey, Blocking: true,
	})
}

func (b *expansionBuilder) hasBlockingIssue() bool {
	for _, issue := range b.issues {
		if issue.Blocking {
			return true
		}
	}
	return false
}

func (b *expansionBuilder) sortIssues() {
	sort.SliceStable(b.issues, func(i, j int) bool {
		left, right := b.issues[i], b.issues[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		if left.ArtifactKey != right.ArtifactKey {
			return left.ArtifactKey < right.ArtifactKey
		}
		return left.Message < right.Message
	})
}

func normalizedAssembly(input Assembly) Assembly {
	assembly := Assembly{
		Nodes: append([]Node(nil), input.Nodes...),
		Edges: append([]Edge(nil), input.Edges...),
	}
	if assembly.Nodes == nil {
		assembly.Nodes = []Node{}
	}
	if assembly.Edges == nil {
		assembly.Edges = []Edge{}
	}
	sort.SliceStable(assembly.Nodes, func(i, j int) bool { return assembly.Nodes[i].ID < assembly.Nodes[j].ID })
	sort.SliceStable(assembly.Edges, func(i, j int) bool {
		left, right := assembly.Edges[i], assembly.Edges[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Relation != right.Relation {
			return left.Relation < right.Relation
		}
		return left.ID < right.ID
	})
	return assembly
}

func expansionDigest(expansion Expansion) (string, error) {
	raw, err := json.Marshal(expansion)
	if err != nil {
		return "", err
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return "", err
	}
	delete(content, "digest")
	canonical, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func managerResourceSpec(config map[string]any) map[string]any {
	return map[string]any{
		"connector_type": "edge-manager-resource",
		"type_version":   "1",
		"device_id":      "",
		"config":         config,
	}
}

func modelDistributionKey(modelID, gatewayID string) string {
	return strings.Join([]string{ArtifactKindModelDistribution, modelID, gatewayID}, ":")
}

func pipelineRegistrationKey(pipelineID, gatewayID string) string {
	return strings.Join([]string{ArtifactKindPipelineRegistration, pipelineID, gatewayID}, ":")
}

func externalServableKey(externalID, gatewayID string) string {
	return strings.Join([]string{ArtifactKindExternalServable, externalID, gatewayID}, ":")
}

func inferenceBindingKey(cameraID, pipelineID string) string {
	return strings.Join([]string{ArtifactKindInferenceBinding, cameraID, pipelineID}, ":")
}

func companionSubscriptionKey(pipelineID, companionID, gatewayID string) string {
	return strings.Join([]string{ArtifactKindCompanionSubscription, pipelineID, companionID, gatewayID}, ":")
}

func ruleChainKey(ruleID string) string {
	return ArtifactKindRuleChain + ":" + ruleID
}

func northboundChannelKey(sourceID, northboundID string) string {
	return strings.Join([]string{ArtifactKindNorthboundChannel, sourceID, northboundID}, ":")
}

func northboundFilterKey(gatewayID string) string {
	return strings.Join([]string{ArtifactKindNorthboundFilter, gatewayID}, ":")
}

func companionRuleChainKey(companionID string) string {
	return strings.Join([]string{ArtifactKindRuleChain, "companion", companionID}, ":")
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	out := make([]string, 0, len(copyValues))
	for _, value := range copyValues {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func nonemptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
