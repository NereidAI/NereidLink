// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// types.go defines the versioned business assembly and expansion documents.
// These types describe subscriptions and declarative delivery intent only;
// they never describe an executable frame-processing graph.

package business

import "time"

const (
	// TemplateSchemaVersion is the only business template wire version emitted by this package.
	TemplateSchemaVersion = "nereidlink.business-template/v1"

	// ExpansionSchemaVersion is the only business expansion wire version emitted by this package.
	ExpansionSchemaVersion = "nereidlink.business-expansion/v1"
)

const (
	// NodeKindCamera identifies a camera source node.
	NodeKindCamera = "camera"
	// NodeKindPipeline identifies a registered NI pipeline reference.
	NodeKindPipeline = "pipeline"
	// NodeKindModel identifies a managed model dependency.
	NodeKindModel = "model"
	// NodeKindCompanion identifies a companion conclusion service.
	NodeKindCompanion = "companion"
	// NodeKindRule identifies a declarative business rule node.
	NodeKindRule = "rule"
	// NodeKindAction identifies a command or alarm action node.
	NodeKindAction = "action"
	// NodeKindNorthbound identifies an explicit selective egress node.
	NodeKindNorthbound = "northbound"
	// NodeKindExternalServable identifies an external inference dependency.
	NodeKindExternalServable = "external_servable"
)

const (
	// RelationFrames binds camera frames to one pipeline.
	RelationFrames = "frames"
	// RelationUses declares a pipeline model dependency.
	RelationUses = "uses"
	// RelationServes declares a pipeline external-servable dependency.
	RelationServes = "serves"
	// RelationFacts selects pipeline facts for a companion or rule.
	RelationFacts = "facts"
	// RelationConclusions selects companion conclusions for a rule.
	RelationConclusions = "conclusions"
	// RelationTriggers connects a rule to an action.
	RelationTriggers = "triggers"
	// RelationPublishes declares explicit selective northbound output.
	RelationPublishes = "publishes"
)

const (
	// ArtifactKindModelDistribution declares model readiness on a gateway.
	ArtifactKindModelDistribution = "model_distribution"
	// ArtifactKindPipelineRegistration declares an NI pipeline registration.
	ArtifactKindPipelineRegistration = "pipeline_registration"
	// ArtifactKindExternalServable declares an NI external-servable registration.
	ArtifactKindExternalServable = "external_servable"
	// ArtifactKindInferenceBinding declares an existing edge-inference connector.
	ArtifactKindInferenceBinding = "inference_binding"
	// ArtifactKindCompanionSubscription declares a companion fact subscription.
	ArtifactKindCompanionSubscription = "companion_subscription"
	// ArtifactKindRuleChain declares a closed declarative rule graph.
	ArtifactKindRuleChain = "rule_chain"
	// ArtifactKindNorthboundChannel declares one selective egress contribution.
	ArtifactKindNorthboundChannel = "northbound_channel"
	// ArtifactKindNorthboundFilter declares the gateway-level NE filter singleton.
	ArtifactKindNorthboundFilter = "northbound_filter"
)

// Template is an immutable, versioned business assembly definition. Requires
// maps requirement kinds to symbolic refs; concrete asset identities belong to
// an instance assembly, not to a reusable template. Templates are data
// artifacts imported as versioned packs; the binary ships no code-owned
// business vocabulary.
type Template struct {
	// SchemaVersion identifies the template wire contract.
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`
	// ID is the stable template identifier.
	ID string `json:"id" yaml:"id"`
	// Version is the immutable template version.
	Version string `json:"version" yaml:"version"`
	// Name is the operator-facing template name.
	Name string `json:"name" yaml:"name"`
	// Description summarizes the business assembly without runtime semantics.
	Description string `json:"description" yaml:"description"`
	// Requires maps requirement kinds to symbolic refs.
	Requires map[string]string `json:"requires" yaml:"requires"`
	// Parameters is the JSON Schema object for instance values.
	Parameters map[string]any `json:"parameters" yaml:"parameters"`
	// Assembly is the template's placeholder relationship graph.
	Assembly Assembly `json:"assembly" yaml:"assembly"`
	// CreatedAt is optional persistence metadata and does not affect expansion.
	CreatedAt *time.Time `json:"created_at,omitempty" yaml:"created_at,omitempty"`
}

// Assembly is a business relationship graph. It is not a frame DAG.
type Assembly struct {
	// Nodes is the complete business participant set, always an array.
	Nodes []Node `json:"nodes" yaml:"nodes"`
	// Edges is the permitted relationship set, always an array.
	Edges []Edge `json:"edges" yaml:"edges"`
}

// Node selects one business participant and its instance-local asset settings.
type Node struct {
	// ID is the stable assembly-local node identifier used in artifact keys.
	ID string `json:"id" yaml:"id"`
	// Kind is one closed business node-kind value.
	Kind string `json:"kind" yaml:"kind"`
	// Name is the operator-facing node name.
	Name string `json:"name" yaml:"name"`
	// Ref is a symbolic requirement or registered pipeline reference.
	Ref string `json:"ref" yaml:"ref"`
	// Asset contains instance-local references and scalar deployment settings.
	Asset map[string]any `json:"asset" yaml:"asset"`
	// Position stores only canvas layout.
	Position Position `json:"position" yaml:"position"`
}

// Position is a canvas coordinate with no runtime semantics.
type Position struct {
	// X is the horizontal canvas coordinate.
	X float64 `json:"x" yaml:"x"`
	// Y is the vertical canvas coordinate.
	Y float64 `json:"y" yaml:"y"`
}

// Edge declares one permitted subscription, dependency, trigger, or publish relation.
type Edge struct {
	// ID is the stable assembly-local edge identifier.
	ID string `json:"id" yaml:"id"`
	// Source is the source node ID.
	Source string `json:"source" yaml:"source"`
	// Target is the target node ID.
	Target string `json:"target" yaml:"target"`
	// Relation is the closed relationship label for the node-kind pair.
	Relation string `json:"relation" yaml:"relation"`
}

// ExpansionInput is the complete instance-local input to pure expansion.
type ExpansionInput struct {
	// InstanceID identifies the desired business instance.
	InstanceID string `json:"instance_id"`
	// Generation fences this desired expansion snapshot.
	Generation int64 `json:"generation"`
	// Parameters contains instance values validated against the template schema.
	Parameters map[string]any `json:"parameters"`
	// Assembly contains the instance's complete concrete relationship graph.
	Assembly Assembly `json:"assembly"`
}

// TemplateRef identifies the immutable template version used for an expansion.
type TemplateRef struct {
	// ID is the source template identifier.
	ID string `json:"id"`
	// Version is the exact source template version.
	Version string `json:"version"`
}

// ValidationReport contains deterministic document-validation findings.
type ValidationReport struct {
	// Valid is true only when no blocking issue exists.
	Valid bool `json:"valid"`
	// Issues is the deterministically ordered finding set, always an array.
	Issues []Issue `json:"issues"`
}

// Issue is one machine-readable validation finding. Blocking issues make the
// report invalid; an artifact key is present when the finding concerns one output.
type Issue struct {
	// Code is the stable machine-readable finding identifier.
	Code string `json:"code"`
	// Message explains the finding to an operator.
	Message string `json:"message"`
	// NodeID optionally locates the finding in the assembly.
	NodeID string `json:"node_id,omitempty"`
	// ArtifactKey optionally locates the finding in the expansion.
	ArtifactKey string `json:"artifact_key,omitempty"`
	// Blocking marks a finding that prevents deployment.
	Blocking bool `json:"blocking"`
}

// Expansion is the complete declarative delivery plan for one desired generation.
type Expansion struct {
	// SchemaVersion identifies the expansion wire contract.
	SchemaVersion string `json:"schema_version"`
	// InstanceID identifies the expanded business instance.
	InstanceID string `json:"instance_id"`
	// Generation is the desired generation covered by this snapshot.
	Generation int64 `json:"generation"`
	// Template identifies the exact immutable source version.
	Template TemplateRef `json:"template"`
	// Digest is SHA-256 of canonical content with this field excluded.
	Digest string `json:"digest"`
	// Artifacts is the key-sorted declarative delivery set, always an array.
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one stable, declarative request routed to its existing owner.
type Artifact struct {
	// Key is stable across generations and rooted in assembly node IDs.
	Key string `json:"key"`
	// Kind is one closed declarative artifact-kind value.
	Kind string `json:"kind"`
	// GatewayID optionally selects the destination gateway.
	GatewayID string `json:"gateway_id,omitempty"`
	// DeviceID optionally selects the destination or conclusion device.
	DeviceID string `json:"device_id,omitempty"`
	// DependsOn records sorted delivery-order dependencies, always an array.
	DependsOn []string `json:"depends_on"`
	// Spec is the complete owner-specific declarative request object.
	Spec map[string]any `json:"spec"`
}
