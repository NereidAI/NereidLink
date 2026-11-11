// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validate.go implements hermetic document checks: JSON Schema parameters,
// the closed assembly relationship vocabulary, concrete instance assets,
// acyclicity, and selective northbound safety.

package business

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateTemplate checks a custom template version before it is persisted.
// Assets are instance-owned, so only metadata, the parameter schema and the
// closed relationship vocabulary are structural requirements here.
func ValidateTemplate(template Template) ValidationReport {
	builder := expansionBuilder{
		assembly: normalizedAssembly(template.Assembly),
		nodes:    map[string]Node{},
		issues:   make([]Issue, 0),
	}
	if template.SchemaVersion != "" && template.SchemaVersion != TemplateSchemaVersion {
		builder.addIssue("invalid_schema_version",
			fmt.Sprintf("schema_version must be %q", TemplateSchemaVersion), "", "")
	}
	if strings.TrimSpace(template.ID) == "" {
		builder.addIssue("missing_id", "template id is required", "", "")
	}
	if strings.TrimSpace(template.Version) == "" {
		builder.addIssue("missing_version", "template version is required", "", "")
	}
	if strings.TrimSpace(template.Name) == "" {
		builder.addIssue("missing_name", "template name is required", "", "")
	}
	if err := compileParameterSchema(template.Parameters); err != nil {
		builder.addIssue("invalid_parameter_schema", err.Error(), "", "")
	}
	builder.validateAssemblyStructure()
	builder.sortIssues()
	return ValidationReport{Valid: !builder.hasBlockingIssue(), Issues: builder.issues}
}

func compileParameterSchema(parameters map[string]any) error {
	document := any(map[string]any{"type": "object"})
	if len(parameters) > 0 {
		document = parameters
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode parameter schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	// Inline schemas are self-contained; validation never resolves network refs.
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are not supported")
	}
	const resource = "inline://business-template-parameters.json"
	if err := compiler.AddResource(resource, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("load parameter schema: %w", err)
	}
	if _, err := compiler.Compile(resource); err != nil {
		return fmt.Errorf("compile parameter schema: %w", err)
	}
	return nil
}

func (b *expansionBuilder) validateParameters() map[string]any {
	effective := map[string]any{}
	for name, schema := range schemaProperties(b.template.Parameters) {
		if object, ok := schema.(map[string]any); ok {
			if value, exists := object["default"]; exists {
				effective[name] = value
			}
		}
	}
	for name, value := range b.input.Parameters {
		effective[name] = value
	}

	if b.template.Parameters == nil {
		b.addIssue("invalid_parameter_schema", "template parameters must be a JSON Schema object", "", "")
	}
	schemaDocument := any(map[string]any{"type": "object"})
	if len(b.template.Parameters) > 0 {
		schemaDocument = b.template.Parameters
	}
	schemaRaw, err := json.Marshal(schemaDocument)
	if err != nil {
		b.addIssue("invalid_parameter_schema", fmt.Sprintf("encode parameter schema: %v", err), "", "")
		return effective
	}
	compiler := jsonschema.NewCompiler()
	// Inline schemas are self-contained; validation never resolves network refs.
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are not supported")
	}
	const resource = "inline://business-template-parameters.json"
	if err := compiler.AddResource(resource, bytes.NewReader(schemaRaw)); err != nil {
		b.addIssue("invalid_parameter_schema", fmt.Sprintf("load parameter schema: %v", err), "", "")
		return effective
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		b.addIssue("invalid_parameter_schema", fmt.Sprintf("compile parameter schema: %v", err), "", "")
		return effective
	}
	parametersRaw, err := json.Marshal(effective)
	if err != nil {
		b.addIssue("invalid_parameters", fmt.Sprintf("encode instance parameters: %v", err), "", "")
		return effective
	}
	var document any
	if err := json.Unmarshal(parametersRaw, &document); err != nil {
		b.addIssue("invalid_parameters", fmt.Sprintf("decode instance parameters: %v", err), "", "")
		return effective
	}
	if err := compiled.Validate(document); err != nil {
		b.addIssue("invalid_parameters", fmt.Sprintf("instance parameters violate template schema: %v", err), "", "")
	}
	return effective
}

func schemaProperties(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	return properties
}

func (b *expansionBuilder) validateAssembly() {
	b.validateAssemblyStructure()
	b.validateAssets()
}

// validateAssemblyStructure checks only the closed relationship vocabulary and
// graph shape. It is reused by template creation, where concrete assets are
// intentionally supplied later by each instance.
func (b *expansionBuilder) validateAssemblyStructure() {
	edgeIDs := map[string]struct{}{}
	outgoingCamera := map[string]int{}
	for _, node := range b.assembly.Nodes {
		if node.ID == "" {
			b.addIssue("invalid_node", "assembly node id is required", "", "")
			continue
		}
		if _, exists := b.nodes[node.ID]; exists {
			b.addIssue("duplicate_node", "assembly node id must be unique", node.ID, "")
			continue
		}
		b.nodes[node.ID] = node
		if !knownNodeKind(node.Kind) {
			b.addIssue("unknown_node_kind", fmt.Sprintf("unknown assembly node kind %q", node.Kind), node.ID, "")
		}
		for key := range node.Asset {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "script", "javascript", "expression", "code":
				b.addIssue("executable_content", fmt.Sprintf("assembly asset %q is not declarative", key), node.ID, "")
			}
		}
	}

	for _, edge := range b.assembly.Edges {
		if edge.ID == "" {
			b.addIssue("invalid_edge", "assembly edge id is required", "", "")
		} else if _, exists := edgeIDs[edge.ID]; exists {
			b.addIssue("duplicate_edge", "assembly edge id must be unique", edge.Source, "")
		} else {
			edgeIDs[edge.ID] = struct{}{}
		}
		source, sourceOK := b.nodes[edge.Source]
		target, targetOK := b.nodes[edge.Target]
		if !sourceOK || !targetOK {
			b.addIssue("dangling_edge", fmt.Sprintf("edge %q references an unknown source or target", edge.ID), "", "")
			continue
		}
		if !allowedBusinessEdge(source.Kind, target.Kind, edge.Relation) {
			b.addIssue("invalid_edge", fmt.Sprintf("edge %q has disallowed relation %s -%s-> %s", edge.ID, source.Kind, edge.Relation, target.Kind), source.ID, "")
			continue
		}
		if source.Kind == NodeKindCamera {
			outgoingCamera[source.ID]++
		}
	}
	for cameraID, count := range outgoingCamera {
		if count > 1 {
			b.addIssue("camera_multiple_pipelines", "one camera may bind only one pipeline in v1", cameraID, "")
		}
	}
	if cycle := findCycle(b.assembly, b.nodes); cycle {
		b.addIssue("assembly_cycle", "business assembly must be acyclic", "", "")
	}
}

func knownNodeKind(kind string) bool {
	switch kind {
	case NodeKindCamera, NodeKindPipeline, NodeKindModel, NodeKindCompanion,
		NodeKindRule, NodeKindAction, NodeKindNorthbound, NodeKindExternalServable:
		return true
	default:
		return false
	}
}

func allowedBusinessEdge(sourceKind, targetKind, relation string) bool {
	switch {
	case sourceKind == NodeKindCamera && targetKind == NodeKindPipeline:
		return relation == RelationFrames
	case sourceKind == NodeKindModel && targetKind == NodeKindPipeline:
		return relation == RelationUses
	case sourceKind == NodeKindExternalServable && targetKind == NodeKindPipeline:
		return relation == RelationServes
	case sourceKind == NodeKindPipeline && targetKind == NodeKindCompanion:
		return relation == RelationFacts
	case sourceKind == NodeKindPipeline && targetKind == NodeKindRule:
		return relation == RelationFacts
	case sourceKind == NodeKindCompanion && targetKind == NodeKindRule:
		return relation == RelationConclusions
	case sourceKind == NodeKindRule && targetKind == NodeKindAction:
		return relation == RelationTriggers
	case sourceKind == NodeKindRule && targetKind == NodeKindNorthbound:
		return relation == RelationPublishes
	case sourceKind == NodeKindCompanion && targetKind == NodeKindNorthbound:
		return relation == RelationPublishes
	default:
		return false
	}
}

func findCycle(assembly Assembly, nodes map[string]Node) bool {
	indegree := map[string]int{}
	adjacency := map[string][]string{}
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range assembly.Edges {
		if _, ok := nodes[edge.Source]; !ok {
			continue
		}
		if _, ok := nodes[edge.Target]; !ok {
			continue
		}
		adjacency[edge.Source] = append(adjacency[edge.Source], edge.Target)
		indegree[edge.Target]++
	}
	queue := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range adjacency[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return visited != len(nodes)
}

func (b *expansionBuilder) validateAssets() {
	required := map[string][]string{
		NodeKindCamera:           {"gateway_id", "device_id"},
		NodeKindPipeline:         {"pipeline_id"},
		NodeKindModel:            {"model_id", "version"},
		NodeKindCompanion:        {"companion_id", "conclusion_device_id"},
		NodeKindExternalServable: {"servable_id"},
	}
	for _, node := range b.assembly.Nodes {
		for _, field := range required[node.Kind] {
			if assetString(node, field) == "" {
				b.addIssue("missing_asset", fmt.Sprintf("%s asset %s is required", node.Kind, field), node.ID, "")
			}
		}
		switch node.Kind {
		case NodeKindCamera:
			if _, exists := node.Asset["publish_readings"]; exists {
				if _, ok := assetStringsStrict(node, "publish_readings"); !ok {
					b.addIssue("invalid_asset", "camera asset publish_readings must be an array of strings", node.ID, "")
				}
			}
		case NodeKindModel:
			// Optional nh-model-distribution/v1 envelope overrides. The
			// closed precision enum is hardware-aware selection expressed as
			// data; no per-silicon branch may appear in the kernel.
			if precision := assetString(node, "precision"); precision != "" &&
				precision != "fp16" && precision != "int8" {
				b.addIssue("invalid_asset",
					"model asset precision must be fp16 or int8", node.ID, "")
			}
			for _, field := range []string{"object_type", "object_id"} {
				if value := assetString(node, field); value != "" && !safeModelAssetToken(value) {
					b.addIssue("invalid_asset",
						fmt.Sprintf("model asset %s must use ESS-safe token characters", field), node.ID, "")
				}
			}
		case NodeKindAction:
			b.validateAction(node)
		case NodeKindNorthbound:
			b.validateNorthbound(node)
		}
	}
}

func (b *expansionBuilder) validateAction(node Node) {
	actionType := actionTypeOf(node)
	if actionType == "" {
		b.addIssue("invalid_asset", "action_type must be send-command or create-alarm", node.ID, "")
		return
	}
	if actionType == "send-command" {
		for _, field := range []string{"target_device_id", "resource"} {
			if assetString(node, field) == "" {
				b.addIssue("missing_asset", fmt.Sprintf("command action asset %s is required", field), node.ID, "")
			}
		}
		if assetString(node, "command_name") == "" && assetString(node, "resource") == "" {
			b.addIssue("missing_asset", "command action command_name or resource is required", node.ID, "")
		}
		return
	}
	for _, field := range []string{"alarm_type", "severity"} {
		if assetString(node, field) == "" {
			b.addIssue("missing_asset", fmt.Sprintf("alarm action asset %s is required", field), node.ID, "")
		}
	}
}

func (b *expansionBuilder) validateNorthbound(node Node) {
	for _, field := range []string{
		"channel", "forwarder_id", "edgex_device_name", "edgex_profile_name",
	} {
		if assetString(node, field) == "" {
			b.addIssue("missing_asset", fmt.Sprintf("northbound asset %s is required", field), node.ID, "")
		}
	}
	readings, ok := assetStringsStrict(node, "readings")
	if !ok || len(readings) == 0 {
		b.addIssue("invalid_northbound_readings", "northbound readings must be a non-empty array of explicit strings", node.ID, "")
		return
	}
	for _, reading := range readings {
		if !validReadingPath(reading) {
			b.addIssue("invalid_northbound_readings", "northbound readings must be non-empty dot paths without empty segments", node.ID, "")
		}
		if strings.Contains(reading, "*") {
			b.addIssue("northbound_wildcard", "northbound readings must not contain wildcards", node.ID, "")
		}
		if containsEmbeddingSegment(reading) {
			b.addIssue("northbound_embeddings", "northbound readings must not include embedding or embeddings path segments", node.ID, "")
		}
	}
	resourceNames, ok := assetStringsStrict(node, "edgex_resource_names")
	if !ok || len(resourceNames) == 0 {
		b.addIssue("invalid_northbound_resources", "edgex_resource_names must be a non-empty array of explicit EdgeX resource names", node.ID, "")
		return
	}
	for _, resourceName := range resourceNames {
		if resourceName == "" || strings.ContainsAny(resourceName, "*?[") {
			b.addIssue("invalid_northbound_resources", "EdgeX resource selectors must be explicit names without path.Match patterns", node.ID, "")
		}
		if resourceName == "PipelineJSON" {
			b.addIssue("northbound_pipeline_json", "PipelineJSON is always dropped by the NE privacy guard", node.ID, "")
		}
		if strings.HasPrefix(resourceName, "Embedding") {
			b.addIssue("northbound_embeddings", "EdgeX resources with the case-sensitive Embedding prefix are always dropped by NE", node.ID, "")
		}
	}
}

func validReadingPath(path string) bool {
	if path == "" {
		return false
	}
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return false
		}
	}
	return true
}

func containsEmbeddingSegment(path string) bool {
	for _, segment := range strings.Split(path, ".") {
		if strings.EqualFold(strings.TrimSpace(segment), "embedding") || strings.EqualFold(strings.TrimSpace(segment), "embeddings") {
			return true
		}
	}
	return false
}

func (b *expansionBuilder) sortedNodes(kind string) []Node {
	nodes := make([]Node, 0)
	for _, node := range b.assembly.Nodes {
		if node.Kind == kind {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func assetString(node Node, key string) string {
	value, _ := node.Asset[key].(string)
	return strings.TrimSpace(value)
}

// safeModelAssetToken mirrors NH's isSafeObjectToken rule for the optional
// model-asset ESS identity overrides: word characters plus . - ~ ! @ # $ ^ *.
func safeModelAssetToken(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(".-~!@#$^*_", r):
		default:
			return false
		}
	}
	return true
}

func assetStrings(node Node, key string) []string {
	values, ok := assetStringsStrict(node, key)
	if !ok {
		return []string{}
	}
	return values
}

func assetStringsStrict(node Node, key string) ([]string, bool) {
	raw, exists := node.Asset[key]
	if !exists {
		return []string{}, false
	}
	values := []string{}
	appendValue := func(value any) bool {
		text, ok := value.(string)
		if !ok {
			return false
		}
		values = append(values, strings.TrimSpace(text))
		return true
	}
	switch typed := raw.(type) {
	case []string:
		for _, value := range typed {
			if !appendValue(value) {
				return []string{}, false
			}
		}
	case []any:
		for _, value := range typed {
			if !appendValue(value) {
				return []string{}, false
			}
		}
	default:
		return []string{}, false
	}
	return values, true
}

func assetObject(node Node, key string) map[string]any {
	if object, ok := node.Asset[key].(map[string]any); ok {
		return object
	}
	if object, ok := node.Asset[key].(map[string]string); ok {
		converted := make(map[string]any, len(object))
		for name, value := range object {
			converted[name] = value
		}
		return converted
	}
	return map[string]any{}
}

func assetFloat(node Node, key string, fallback float64) float64 {
	value, ok := numericFloat(node.Asset[key])
	if !ok {
		return fallback
	}
	return value
}

func assetInt(node Node, key string, fallback int64) int64 {
	value, ok := numericFloat(node.Asset[key])
	if !ok {
		return fallback
	}
	return int64(value)
}

func assetBool(node Node, key string, fallback bool) bool {
	value, ok := node.Asset[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func parameterFloat(parameters map[string]any, key string, fallback float64) float64 {
	value, ok := numericFloat(parameters[key])
	if !ok {
		return fallback
	}
	return value
}

func parameterInt(parameters map[string]any, key string, fallback int64) int64 {
	value, ok := numericFloat(parameters[key])
	if !ok {
		return fallback
	}
	return int64(value)
}

func parameterString(parameters map[string]any, key, fallback string) string {
	value, ok := parameters[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func parameterBool(parameters map[string]any, key string, fallback bool) bool {
	value, ok := parameters[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func numericFloat(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case json.Number:
		parsed, err := strconv.ParseFloat(value.String(), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
