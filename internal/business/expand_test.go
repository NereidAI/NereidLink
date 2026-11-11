// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package business

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/rule"
)

var updateGolden = flag.Bool("update", false, "update business expansion golden snapshots")

func TestSamplePackExpansionGolden(t *testing.T) {
	for _, test := range []struct {
		templateID string
		golden     string
	}{
		{templateID: "face-access-control", golden: "face_access_control.golden.json"},
		{templateID: "meter-reading", golden: "meter_reading.golden.json"},
	} {
		t.Run(test.templateID, func(t *testing.T) {
			template, input := validTemplateInput(t, test.templateID)
			expansion, report := Expand(template, input)
			if !report.Valid {
				t.Fatalf("valid instance rejected: %+v", report.Issues)
			}
			raw, err := json.MarshalIndent(expansion, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			path := filepath.Join("testdata", test.golden)
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, expected) {
				t.Fatalf("expansion differs from %s\nactual:\n%s", path, raw)
			}
			assertDigest(t, expansion)
		})
	}
}

func TestFacePackExpandsDetectorAndEmbeddingModels(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	if template.Version != "0.0.1" {
		t.Fatalf("face template version = %q, want 0.0.1", template.Version)
	}
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("valid instance rejected: %+v", report.Issues)
	}
	models := map[string]string{}
	for _, artifact := range expansion.Artifacts {
		if artifact.Kind == ArtifactKindModelDistribution {
			models[artifact.Key] = artifact.Spec["model_id"].(string)
		}
	}
	if len(models) != 2 ||
		models["model_distribution:model:gateway-1"] != "face-detector" ||
		models["model_distribution:model-embedding:gateway-1"] != "face-embedding" {
		t.Fatalf("face model artifacts = %+v, want detector and embedding", models)
	}
	for _, kind := range []string{ArtifactKindPipelineRegistration, ArtifactKindInferenceBinding} {
		artifact := findArtifact(t, expansion, kind)
		for _, key := range []string{
			"model_distribution:model:gateway-1",
			"model_distribution:model-embedding:gateway-1",
		} {
			if !slices.Contains(artifact.DependsOn, key) {
				t.Errorf("%s does not depend on %s: %+v", kind, key, artifact.DependsOn)
			}
		}
	}
}

func TestModelArtifactCarriesOptionalNHEnvelopeOverrides(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	for index := range input.Assembly.Nodes {
		if input.Assembly.Nodes[index].Kind != NodeKindModel {
			continue
		}
		input.Assembly.Nodes[index].Asset["precision"] = "int8"
		input.Assembly.Nodes[index].Asset["object_type"] = "face-package"
		input.Assembly.Nodes[index].Asset["object_id"] = "yunet-int8"
	}
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("valid overrides rejected: %+v", report.Issues)
	}
	for _, artifact := range expansion.Artifacts {
		if artifact.Kind != ArtifactKindModelDistribution {
			continue
		}
		if artifact.Spec["precision"] != "int8" ||
			artifact.Spec["object_type"] != "face-package" ||
			artifact.Spec["object_id"] != "yunet-int8" {
			t.Fatalf("optional NH envelope fields not emitted: %+v", artifact.Spec)
		}
	}

	// Invalid precision and unsafe ESS tokens are document-level findings.
	_, badPrecision := validTemplateInput(t, "face-access-control")
	for index := range badPrecision.Assembly.Nodes {
		if badPrecision.Assembly.Nodes[index].Kind == NodeKindModel {
			badPrecision.Assembly.Nodes[index].Asset["precision"] = "int4"
			break
		}
	}
	if _, report := Expand(template, badPrecision); report.Valid {
		t.Fatal("invalid precision must be rejected")
	}

	_, badToken := validTemplateInput(t, "face-access-control")
	for index := range badToken.Assembly.Nodes {
		if badToken.Assembly.Nodes[index].Kind == NodeKindModel {
			badToken.Assembly.Nodes[index].Asset["object_id"] = "../escape"
			break
		}
	}
	if _, report := Expand(template, badToken); report.Valid {
		t.Fatal("unsafe object id must be rejected")
	}
}

func TestExpansionDeterministicAndStableKeys(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	first, firstReport := Expand(template, input)
	for left, right := 0, len(input.Assembly.Nodes)-1; left < right; left, right = left+1, right-1 {
		input.Assembly.Nodes[left], input.Assembly.Nodes[right] = input.Assembly.Nodes[right], input.Assembly.Nodes[left]
	}
	for left, right := 0, len(input.Assembly.Edges)-1; left < right; left, right = left+1, right-1 {
		input.Assembly.Edges[left], input.Assembly.Edges[right] = input.Assembly.Edges[right], input.Assembly.Edges[left]
	}
	second, secondReport := Expand(template, input)
	if !firstReport.Valid || !secondReport.Valid {
		t.Fatalf("valid instance rejected: %+v %+v", firstReport.Issues, secondReport.Issues)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) || first.Digest != second.Digest {
		t.Fatal("identical input did not produce byte-identical JSON and digest")
	}

	input.Generation++
	nextGeneration, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("next generation rejected: %+v", report.Issues)
	}
	if first.Digest == nextGeneration.Digest {
		t.Fatal("generation must participate in the expansion digest")
	}
	if strings.Join(artifactKeys(first), "\n") != strings.Join(artifactKeys(nextGeneration), "\n") {
		t.Fatal("artifact keys changed with generation")
	}
}

func TestInferenceBindingUsesPipelineAndOutputDevice(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("valid instance rejected: %+v", report.Issues)
	}
	binding := findArtifact(t, expansion, ArtifactKindInferenceBinding)
	if binding.DeviceID != "face-output-1" || binding.Spec["device_id"] != "face-output-1" {
		t.Fatalf("binding does not target the output virtual device: %+v", binding)
	}
	config := binding.Spec["config"].(map[string]any)
	if config["PipelineId"] != "face-recognition-v1" {
		t.Fatalf("draft PipelineId missing: %+v", config)
	}
	if _, exists := config["PipelineID"]; exists {
		t.Fatal("binding emitted PipelineID instead of draft PipelineId")
	}
	if config["SourceDevice"] != "face-camera-1" {
		t.Fatalf("SourceDevice did not fall back to the camera device_id: %+v", config)
	}
	if readings, ok := config["PublishReadings"].([]string); !ok || readings == nil {
		t.Fatalf("PublishReadings must be a non-nil array: %#v", config["PublishReadings"])
	}
}

func TestNorthboundRejectsEmbeddingsAndWildcards(t *testing.T) {
	for _, test := range []struct {
		name    string
		reading string
		code    string
	}{
		{name: "case insensitive path segment", reading: "payload.EmbEdDiNgS", code: "northbound_embeddings"},
		{name: "single embedding segment", reading: "embedding", code: "northbound_embeddings"},
		{name: "wildcard", reading: "value.*", code: "northbound_wildcard"},
		{name: "empty path", reading: "", code: "invalid_northbound_readings"},
	} {
		t.Run(test.name, func(t *testing.T) {
			template, input := validTemplateInput(t, "meter-reading")
			setAsset(t, &input.Assembly, "northbound", "readings", []string{"value", test.reading})
			expansion, report := Expand(template, input)
			if report.Valid || !hasIssue(report, test.code) {
				t.Fatalf("expected %s, got %+v", test.code, report.Issues)
			}
			if expansion.Digest == "" || len(expansion.Artifacts) == 0 {
				t.Fatal("invalid input must still return a partial expansion")
			}
		})
	}
}

func TestNorthboundRejectsForbiddenEdgeXResources(t *testing.T) {
	for _, test := range []struct {
		name     string
		resource string
		code     string
	}{
		{name: "pipeline json", resource: "PipelineJSON", code: "northbound_pipeline_json"},
		{name: "case sensitive embedding prefix", resource: "EmbeddingVector", code: "northbound_embeddings"},
		{name: "pattern", resource: "matched*", code: "invalid_northbound_resources"},
	} {
		t.Run(test.name, func(t *testing.T) {
			template, input := validTemplateInput(t, "meter-reading")
			setAsset(t, &input.Assembly, "northbound", "edgex_resource_names", []string{test.resource})
			_, report := Expand(template, input)
			if report.Valid || !hasIssue(report, test.code) {
				t.Fatalf("expected %s, got %+v", test.code, report.Issues)
			}
		})
	}
}

func TestNorthboundFilterUsesRawEdgeXSelectorsAndGatewaySingleton(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("valid expansion rejected: %+v", report.Issues)
	}
	filters := findArtifacts(expansion, ArtifactKindNorthboundFilter)
	if len(filters) != 1 {
		t.Fatalf("expected one gateway filter singleton, got %d", len(filters))
	}
	filter := filters[0]
	if filter.GatewayID != "gateway-1" || filter.DeviceID != "" {
		t.Fatalf("filter must be gateway-level: %+v", filter)
	}
	config := specMap(filter.Spec, "config")
	rules, ok := config["rules"].([]map[string]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("expected allow and deny rules, got %#v", config["rules"])
	}
	allow := rules[0]
	if allow["action"] != "allow" ||
		!slices.Equal(allow["device_names"].([]string), []string{"face-access-control-edgex-conclusions"}) ||
		!slices.Equal(allow["profile_names"].([]string), []string{"face-access-control-conclusion-profile"}) ||
		!slices.Equal(allow["resource_names"].([]string), []string{"matched", "personId", "score"}) {
		t.Fatalf("raw EdgeX allow selector = %+v", allow)
	}
}

func TestStatelessTemplateAllowsRuleToNorthboundWithoutActions(t *testing.T) {
	template := Template{
		SchemaVersion: TemplateSchemaVersion, ID: "stateless-egress", Version: "1.0.0",
		Name:       "Stateless egress",
		Parameters: map[string]any{"type": "object", "additionalProperties": false},
		Assembly: Assembly{Nodes: []Node{
			{ID: "camera", Kind: NodeKindCamera, Name: "Camera", Asset: map[string]any{"gateway_id": "gateway-1", "device_id": "camera-1"}},
			{ID: "pipeline", Kind: NodeKindPipeline, Name: "Pipeline", Asset: map[string]any{"pipeline_id": "pipeline-1"}},
			{ID: "rule", Kind: NodeKindRule, Name: "Egress rule", Asset: map[string]any{}},
			{ID: "northbound", Kind: NodeKindNorthbound, Name: "Northbound", Asset: map[string]any{
				"channel": "generic.v1", "forwarder_id": "forwarder-1",
				"readings": []string{"value"}, "edgex_device_name": "generic-conclusions",
				"edgex_profile_name": "generic-profile", "edgex_resource_names": []string{"value"},
			}},
		}, Edges: []Edge{
			{ID: "camera-pipeline", Source: "camera", Target: "pipeline", Relation: RelationFrames},
			{ID: "pipeline-rule", Source: "pipeline", Target: "rule", Relation: RelationFacts},
			{ID: "rule-northbound", Source: "rule", Target: "northbound", Relation: RelationPublishes},
		}},
	}
	if report := ValidateTemplate(template); !report.Valid {
		t.Fatalf("stateless template without companion or action nodes rejected: %+v", report.Issues)
	}

	input := ExpansionInput{InstanceID: "stateless-egress-instance", Generation: 1, Assembly: template.Assembly}
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("stateless expansion rejected: %+v", report.Issues)
	}
	chain := findArtifact(t, expansion, ArtifactKindRuleChain)
	graph, ok := chain.Spec["graph"].(rule.GraphDTO)
	if !ok || len(graph.Nodes) != 1 || graph.Nodes[0].Type != rule.NodeTypeForward {
		t.Fatalf("expected one forward-only rule node, got %#v", chain.Spec["graph"])
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("forward-only graph must not create action edges: %#v", graph.Edges)
	}
	findArtifact(t, expansion, ArtifactKindNorthboundChannel)
	findArtifact(t, expansion, ArtifactKindNorthboundFilter)
}

func TestCompanionDirectNorthboundGeneratesForwardOnlyRuleChain(t *testing.T) {
	template := Template{
		SchemaVersion: TemplateSchemaVersion, ID: "custom-companion-egress", Version: "1.0.0",
		Name:       "Companion egress",
		Parameters: map[string]any{"type": "object", "additionalProperties": false},
		Assembly: Assembly{Nodes: []Node{
			{ID: "camera", Kind: NodeKindCamera, Name: "Camera", Asset: map[string]any{"gateway_id": "gateway-1", "device_id": "camera-1"}},
			{ID: "pipeline", Kind: NodeKindPipeline, Name: "Pipeline", Asset: map[string]any{"pipeline_id": "pipeline-1"}},
			{ID: "model", Kind: NodeKindModel, Name: "Model", Asset: map[string]any{"model_id": "model-1", "version": "1.0.0"}},
			{ID: "companion", Kind: NodeKindCompanion, Name: "Companion", Asset: map[string]any{
				"companion_id": "companion-1", "conclusion_device_id": "conclusions-1",
			}},
			{ID: "northbound", Kind: NodeKindNorthbound, Name: "Egress", Asset: map[string]any{
				"channel": "companion.v1", "forwarder_id": "forwarder-1",
				"readings": []string{"value"}, "edgex_device_name": "edgex-conclusions",
				"edgex_profile_name": "conclusion-profile", "edgex_resource_names": []string{"value"},
			}},
		}, Edges: []Edge{
			{ID: "camera-pipeline", Source: "camera", Target: "pipeline", Relation: RelationFrames},
			{ID: "model-pipeline", Source: "model", Target: "pipeline", Relation: RelationUses},
			{ID: "pipeline-companion", Source: "pipeline", Target: "companion", Relation: RelationFacts},
			{ID: "companion-northbound", Source: "companion", Target: "northbound", Relation: RelationPublishes},
		}},
	}
	input := ExpansionInput{InstanceID: "companion-egress-instance", Generation: 1, Assembly: template.Assembly}
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("direct companion egress rejected: %+v", report.Issues)
	}
	chain := findArtifact(t, expansion, ArtifactKindRuleChain)
	if chain.Key != "rule_chain:companion:companion" || chain.DeviceID != "conclusions-1" {
		t.Fatalf("unexpected companion forward chain: %+v", chain)
	}
	graph, ok := chain.Spec["graph"].(rule.GraphDTO)
	if !ok || len(graph.Nodes) != 1 || graph.Nodes[0].Type != rule.NodeTypeForward {
		t.Fatalf("expected one forward-only node, got %#v", chain.Spec["graph"])
	}
	channel := findArtifact(t, expansion, ArtifactKindNorthboundChannel)
	if channel.GatewayID != "gateway-1" || !slices.Contains(channel.DependsOn, "rule_chain:companion:companion") {
		t.Fatalf("companion channel not bound to generated chain: %+v", channel)
	}
}

func TestExternalServableRejectsTargetDevice(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	expansion, report := Expand(template, input)
	if report.Valid || !hasIssue(report, "external_target_device") {
		t.Fatalf("expected external TargetDevice rejection, got %+v", report.Issues)
	}
	binding := findArtifact(t, expansion, ArtifactKindInferenceBinding)
	config := binding.Spec["config"].(map[string]any)
	if config["ServableID"] != "external-face-1" {
		t.Fatalf("external ServableID missing: %+v", config)
	}
}

func TestExternalServableAllowsEmptyTargetDevice(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	setAsset(t, &input.Assembly, "camera", "target_device", "")
	_, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("empty external TargetDevice rejected: %+v", report.Issues)
	}
}

func TestManagerArtifactsUseConnectorV1Envelopes(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	setAsset(t, &input.Assembly, "camera", "target_device", "")
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("valid manager references rejected: %+v", report.Issues)
	}

	pipeline := findArtifact(t, expansion, ArtifactKindPipelineRegistration)
	if pipeline.Spec["connector_type"] != "edge-manager-resource" ||
		pipeline.Spec["type_version"] != "1" || pipeline.Spec["device_id"] != "" {
		t.Fatalf("pipeline connector envelope = %+v", pipeline.Spec)
	}
	pipelineConfig := specMap(pipeline.Spec, "config")
	if pipelineConfig["manager_device"] != "nereid-inference" ||
		pipelineConfig["resource"] != "PipelinesJSON" ||
		pipelineConfig["pipeline_id"] != "face-recognition-v1" {
		t.Fatalf("pipeline manager config = %+v", pipelineConfig)
	}
	if _, exists := pipelineConfig["definition"]; exists {
		t.Fatal("reference-only pipeline invented a definition")
	}

	external := findArtifact(t, expansion, ArtifactKindExternalServable)
	if external.Spec["connector_type"] != "edge-manager-resource" ||
		external.Spec["type_version"] != "1" || external.Spec["device_id"] != "" {
		t.Fatalf("external connector envelope = %+v", external.Spec)
	}
	externalConfig := specMap(external.Spec, "config")
	if externalConfig["manager_device"] != "nereid-inference" ||
		externalConfig["resource"] != "ExternalServablesJSON" ||
		externalConfig["entry_id"] != "external-face-1" ||
		externalConfig["id_field"] != "servableId" {
		t.Fatalf("external manager config = %+v", externalConfig)
	}
	for _, forbidden := range []string{"servable_id", "pipeline_id", "definition"} {
		if _, exists := externalConfig[forbidden]; exists {
			t.Fatalf("reference-only external config contains %s: %+v", forbidden, externalConfig)
		}
	}
	bindingConfig := specMap(findArtifact(t, expansion, ArtifactKindInferenceBinding).Spec, "config")
	if bindingConfig["ServableID"] != "external-face-1" {
		t.Fatalf("inference binding lost ServableID: %+v", bindingConfig)
	}
}

func TestCompanionConsumedBindingRejectsPublishMode(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	setAsset(t, &input.Assembly, "camera", "publish_mode", "all")
	_, report := Expand(template, input)
	if report.Valid || !hasIssue(report, "companion_publish_mode") {
		t.Fatalf("expected companion PublishMode rejection, got %+v", report.Issues)
	}
}

func TestSchemaRejectionReturnsPartialExpansion(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	input.Parameters["threshold"] = "high"
	expansion, report := Expand(template, input)
	if report.Valid || !hasIssue(report, "invalid_parameters") {
		t.Fatalf("expected schema rejection, got %+v", report.Issues)
	}
	if expansion.Digest == "" || len(expansion.Artifacts) == 0 {
		t.Fatal("schema rejection must not panic or suppress the partial expansion")
	}

	template, input = validTemplateInput(t, "meter-reading")
	delete(input.Parameters, "min_value")
	_, report = Expand(template, input)
	if report.Valid || !hasIssue(report, "invalid_parameters") {
		t.Fatalf("expected missing required parameter rejection, got %+v", report.Issues)
	}
}

func TestInvalidEdgeCycleAndMultipleCameraBindings(t *testing.T) {
	t.Run("invalid edge", func(t *testing.T) {
		template, input := validTemplateInput(t, "face-access-control")
		for index := range input.Assembly.Edges {
			if input.Assembly.Edges[index].ID == "model-pipeline" {
				input.Assembly.Edges[index].Relation = RelationFrames
			}
		}
		_, report := Expand(template, input)
		if report.Valid || !hasIssue(report, "invalid_edge") {
			t.Fatalf("expected invalid edge, got %+v", report.Issues)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		template, input := validTemplateInput(t, "face-access-control")
		input.Assembly.Edges = append(input.Assembly.Edges, Edge{
			ID: "cycle", Source: "action-door", Target: "camera", Relation: RelationTriggers,
		})
		_, report := Expand(template, input)
		if report.Valid || !hasIssue(report, "assembly_cycle") {
			t.Fatalf("expected cycle rejection, got %+v", report.Issues)
		}
	})

	t.Run("dangling edge", func(t *testing.T) {
		template, input := validTemplateInput(t, "face-access-control")
		input.Assembly.Edges = append(input.Assembly.Edges, Edge{
			ID: "dangling", Source: "camera", Target: "missing", Relation: RelationFrames,
		})
		_, report := Expand(template, input)
		if report.Valid || !hasIssue(report, "dangling_edge") {
			t.Fatalf("expected dangling edge rejection, got %+v", report.Issues)
		}
	})

	t.Run("one camera one pipeline", func(t *testing.T) {
		template, input := validTemplateInput(t, "face-access-control")
		input.Assembly.Edges = append(input.Assembly.Edges, Edge{
			ID: "second-binding", Source: "camera", Target: "pipeline", Relation: RelationFrames,
		})
		_, report := Expand(template, input)
		if report.Valid || !hasIssue(report, "camera_multiple_pipelines") {
			t.Fatalf("expected multiple binding rejection, got %+v", report.Issues)
		}
	})
}

func TestLegacyFlatManagerArtifactFailsClosed(t *testing.T) {
	artifact := Artifact{
		Key: "pipeline_registration:pipeline:gateway-1", Kind: ArtifactKindPipelineRegistration,
		GatewayID: "gateway-1",
		Spec: map[string]any{
			"manager_device": "nereid-inference", "resource": "PipelinesJSON", "pipeline_id": "p1",
		},
	}
	if _, _, err := managerResourceIdentity(artifact); err == nil {
		t.Fatal("legacy flat manager artifact was silently reinterpreted")
	}
}

func TestPipelineDefinitionPassesThroughAsOpaqueData(t *testing.T) {
	template, input := validTemplateInput(t, "face-access-control")
	definition := map[string]any{
		"nodes": []any{map[string]any{"id": "ni-owned", "kind": "ni-closed-node"}},
	}
	setAsset(t, &input.Assembly, "pipeline", "definition", definition)
	expansion, report := Expand(template, input)
	if !report.Valid {
		t.Fatalf("pipeline definition rejected: %+v", report.Issues)
	}
	artifact := findArtifact(t, expansion, ArtifactKindPipelineRegistration)
	actual, err := json.Marshal(specMap(artifact.Spec, "config")["definition"])
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("opaque pipeline definition changed: %s != %s", actual, expected)
	}
}

func TestSamplePackRuleGraphsCompile(t *testing.T) {
	for _, id := range []string{"face-access-control", "meter-reading"} {
		t.Run(id, func(t *testing.T) {
			template, input := validTemplateInput(t, id)
			expansion, report := Expand(template, input)
			if !report.Valid {
				t.Fatalf("valid instance rejected: %+v", report.Issues)
			}
			artifact := findArtifact(t, expansion, ArtifactKindRuleChain)
			graph, ok := artifact.Spec["graph"].(rule.GraphDTO)
			if !ok {
				t.Fatalf("rule graph has type %T", artifact.Spec["graph"])
			}
			if _, err := rule.Compile("business-test", graph, rule.Deps{}); err != nil {
				t.Fatalf("expanded rule graph does not compile: %v", err)
			}
		})
	}
}

func TestSamplePacksAndExpansionsContainNoScriptsOrTopology(t *testing.T) {
	for _, template := range loadSampleTemplates(t) {
		raw, err := json.Marshal(template)
		if err != nil {
			t.Fatal(err)
		}
		assertNoForbiddenRuleContent(t, raw)
		for _, node := range template.Assembly.Nodes {
			if _, exists := node.Asset["definition"]; exists {
				t.Fatalf("sample pack %s contains NI topology definition", template.ID)
			}
		}

		_, input := validTemplateInput(t, template.ID)
		expansion, report := Expand(template, input)
		if !report.Valid {
			t.Fatalf("valid instance rejected: %+v", report.Issues)
		}
		raw, err = json.Marshal(expansion)
		if err != nil {
			t.Fatal(err)
		}
		assertNoForbiddenRuleContent(t, raw)
		for _, artifact := range expansion.Artifacts {
			if artifact.Kind == ArtifactKindPipelineRegistration {
				if _, exists := specMap(artifact.Spec, "config")["definition"]; exists {
					t.Fatal("sample pack expansion invented an NI topology definition")
				}
			}
		}
	}
}

func validTemplateInput(t *testing.T, id string) (Template, ExpansionInput) {
	t.Helper()
	template := loadSampleTemplate(t, id, sampleTemplateVersion(id))
	input := ExpansionInput{
		InstanceID: id + "-instance",
		Generation: 3,
		Parameters: map[string]any{},
		Assembly:   template.Assembly,
	}
	if id == "face-access-control" {
		input.Parameters = map[string]any{
			"threshold": 0.72, "working_start": "08:00", "working_end": "18:00",
			"debounce_seconds": 3, "unknown_after_seconds": 10, "unknown_alarm": true,
		}
	} else {
		input.Parameters = map[string]any{
			"min_value": 0.0, "max_value": 100.0, "unit": "kWh",
			"stale_after_seconds": 300, "low_confidence_threshold": 0.8, "retry_command": "Capture",
		}
	}
	for index := range input.Assembly.Nodes {
		node := &input.Assembly.Nodes[index]
		switch node.Kind {
		case NodeKindCamera:
			node.Asset = map[string]any{
				"gateway_id": "gateway-1", "device_id": id + "-camera-1",
				"output_device_id": id + "-output-1", "source_kind": "onvif-camera",
				"stream_profile": "main", "decode_accel": "vaapi", "vaapi_device": "renderD128",
				"fps": 5.0, "publish_mode": "", "publish_readings": []string{"PipelineJSON"},
				"target_device": "CPU",
			}
			if id == "face-access-control" {
				node.Asset["device_id"] = "face-camera-1"
				node.Asset["output_device_id"] = "face-output-1"
			}
		case NodeKindPipeline:
			node.Asset["pipeline_id"] = node.Ref
		case NodeKindModel:
			node.Asset["model_id"] = id + "-model-1"
			if id == "face-access-control" {
				node.Asset["model_id"] = node.Ref
			}
			node.Asset["version"] = "1.0.0"
		case NodeKindCompanion:
			node.Asset["companion_id"] = node.Ref
			node.Asset["conclusion_device_id"] = id + "-conclusions-1"
		case NodeKindAction:
			if actionTypeOf(*node) == "send-command" {
				node.Asset["target_device_id"] = id + "-command-target-1"
				node.Asset["params"] = map[string]any{}
				node.Asset["timeout_ms"] = 3000
			}
		case NodeKindNorthbound:
			node.Asset["forwarder_id"] = "forwarder-1"
			node.Asset["edgex_device_name"] = id + "-edgex-conclusions"
			node.Asset["edgex_profile_name"] = id + "-conclusion-profile"
			node.Asset["edgex_resource_names"] = assetStrings(*node, "readings")
		}
	}
	return template, input
}

func addExternalServable(assembly *Assembly) {
	assembly.Nodes = append(assembly.Nodes, Node{
		ID: "external", Kind: NodeKindExternalServable, Name: "External face servable", Ref: "external-face",
		Asset: map[string]any{"servable_id": "external-face-1"}, Position: Position{X: 240, Y: -160},
	})
	assembly.Edges = append(assembly.Edges, Edge{
		ID: "external-pipeline", Source: "external", Target: "pipeline", Relation: RelationServes,
	})
}

func setAsset(t *testing.T, assembly *Assembly, nodeID, key string, value any) {
	t.Helper()
	for index := range assembly.Nodes {
		if assembly.Nodes[index].ID == nodeID {
			assembly.Nodes[index].Asset[key] = value
			return
		}
	}
	t.Fatalf("node %s not found", nodeID)
}

func findArtifact(t *testing.T, expansion Expansion, kind string) Artifact {
	t.Helper()
	artifacts := findArtifacts(expansion, kind)
	if len(artifacts) == 0 {
		t.Fatalf("artifact kind %s not found", kind)
	}
	return artifacts[0]
}

func findArtifacts(expansion Expansion, kind string) []Artifact {
	out := make([]Artifact, 0)
	for _, artifact := range expansion.Artifacts {
		if artifact.Kind == kind {
			out = append(out, artifact)
		}
	}
	return out
}

func hasIssue(report ValidationReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code && issue.Blocking {
			return true
		}
	}
	return false
}

func artifactKeys(expansion Expansion) []string {
	keys := make([]string, 0, len(expansion.Artifacts))
	for _, artifact := range expansion.Artifacts {
		keys = append(keys, artifact.Key)
	}
	return keys
}

func assertDigest(t *testing.T, expansion Expansion) {
	t.Helper()
	raw, err := json.Marshal(expansion)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	delete(content, "digest")
	canonical, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if expansion.Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest does not cover canonical content excluding digest: %s", expansion.Digest)
	}
}

func assertNoForbiddenRuleContent(t *testing.T, raw []byte) {
	t.Helper()
	text := string(raw)
	for _, forbidden := range []string{
		"script-filter", `"switch"`, `"transform"`, `"script"`,
		"params_script", "details_script", "dedupe_key_script",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("forbidden executable rule content %q in %s", forbidden, text)
		}
	}
}
