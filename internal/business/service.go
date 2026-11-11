// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go coordinates the versioned business workflow. Documents and
// expansion stay pure; this file performs live preflight, hands declarative
// artifacts to their existing owners, records owner identities, and derives
// health from read-back rather than from optimistic apply acknowledgements.

package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/egress"
	"github.com/nereidai/nereidlink/internal/modelinventory"
	"github.com/nereidai/nereidlink/internal/registry"
	"github.com/nereidai/nereidlink/internal/rule"
)

// ConnectorOwner is the existing NL connector lifecycle. Business bindings
// create or update one edge-inference instance per camera; they never own the
// connector store themselves.
type ConnectorOwner interface {
	CreateSpec(ctx context.Context, tenantID, gatewayID string, in connector.SaveInput) (connector.Spec, error)
	UpdateSpec(ctx context.Context, tenantID, specID string, in connector.SaveInput) (connector.Spec, error)
	GetSpec(ctx context.Context, tenantID, specID string) (connector.Spec, error)
	DeleteSpec(ctx context.Context, tenantID, specID string) error
}

// RuleOwner is the existing rule-group/version/activation owner.
type RuleOwner interface {
	CreateGroup(ctx context.Context, tenantID, name string) (string, error)
	CreateDraft(ctx context.Context, tenantID, groupID string, dto rule.GraphDTO) (string, rule.GraphDTO, error)
	Activate(ctx context.Context, tenantID, chainID string) error
	SetGroupEnabled(ctx context.Context, tenantID, groupID string, enabled bool) error
	DeleteGroup(ctx context.Context, tenantID, groupID string) error
}

// DeviceOwner rebinds the conclusion virtual device to the generated chain
// group and preserves the previous binding for undeploy.
type DeviceOwner interface {
	GetDevice(ctx context.Context, tenantID, deviceID string) (registry.DeviceDTO, error)
	UpdateDevice(ctx context.Context, tenantID, deviceID string, input registry.DeviceDTO) (registry.DeviceDTO, error)
}

// ForwarderReader validates that an explicitly selected northbound forwarder
// already exists. PublishMode/PublishReadings never imply upload by themselves.
type ForwarderReader interface {
	Get(ctx context.Context, tenantID, id string) (egress.Forwarder, error)
}

// ModelReader exposes the edge-reported model inventory so preflight can
// require the exact READY, non-stale (model_id, version).
type ModelReader interface {
	ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]modelinventory.Model, error)
}

// CompanionOwner is the companion-specific subscription adapter. It is nil
// until each companion owner signs a versioned subscription profile.
type CompanionOwner interface {
	ApplySubscription(ctx context.Context, tenantID string, artifact Artifact) (string, error)
	RemoveSubscription(ctx context.Context, tenantID string, resource Resource) error
}

// DistributionHandle is the NereidHorizon task outcome of one model
// distribution ensure call. It carries no credentials or model bytes.
type DistributionHandle struct {
	// TaskID is the pollable NH distribution task handle.
	TaskID string
	// State mirrors the NH task lifecycle (in_progress/delivered/...).
	State string
	// EdgeReady is true only when every ESS destination is delivered/consumed.
	EdgeReady bool
	// Replay marks an idempotent replay returning an existing active task.
	Replay bool
	// Adopted marks an already-existing verified ESS object taken over as a task.
	Adopted bool
}

// ModelDistributor applies model_distribution artifacts through the
// NereidHorizon idempotent ensure contract (nh-model-distribution/v1). A nil
// owner means the integration is disabled; preflight then emits a blocking
// finding instead of silently keeping the read-only no-op.
type ModelDistributor interface {
	// CheckTarget validates that the artifact maps onto a deliverable NH
	// envelope (precision, ESS destination mapping) without side effects.
	CheckTarget(artifact Artifact) error
	// EnsureDistribution requests ensure and returns the NH task handle.
	// Repeated calls are idempotent at NH and return the same task.
	EnsureDistribution(ctx context.Context, tenantID string, artifact Artifact) (DistributionHandle, error)
}

// Owners bundles the destination owners. Optional owners are deliberately
// nil-able: absence is a blocking preflight finding, never a silent success.
type Owners struct {
	Connectors ConnectorOwner
	Rules      RuleOwner
	Devices    DeviceOwner
	Forwarders ForwarderReader
	Models     ModelReader
	Managers   ConnectorOwner
	Companions CompanionOwner
	Metrics    JobMetricsReader
	// Distribution applies model_distribution artifacts through
	// NereidHorizon. Nil disables automatic model distribution.
	Distribution ModelDistributor
}

// Service implements the business orchestration REST surface.
type Service struct {
	store  Store
	owners Owners
	log    *slog.Logger
	now    func() time.Time
}

var (
	_ ConnectorOwner   = (*connector.Service)(nil)
	_ RuleOwner        = (*rule.ChainStore)(nil)
	_ DeviceOwner      = (*registry.Admin)(nil)
	_ ForwarderReader  = (*egress.Service)(nil)
	_ ModelReader      = (*modelinventory.Service)(nil)
	_ JobMetricsReader = (*connector.Service)(nil)
)

// NewService constructs the orchestration service from its persistence and
// destination owners.
func NewService(store Store, owners Owners, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, owners: owners, log: logger, now: time.Now}
}

// ListTemplates returns every imported template version of the tenant. The
// binary ships no business vocabulary of its own; templates only exist after
// a pack import or a designer publish.
func (s *Service) ListTemplates(ctx context.Context, tenantID string) ([]Template, error) {
	templates, err := s.store.ListTemplates(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(templates, func(i, j int) bool {
		if templates[i].ID != templates[j].ID {
			return templates[i].ID < templates[j].ID
		}
		return templates[i].Version < templates[j].Version
	})
	if templates == nil {
		templates = []Template{}
	}
	return templates, nil
}

// CreateTemplate persists one immutable version. The primary key rejects a
// repeated id@version with ErrConflict; in-place edits do not exist.
func (s *Service) CreateTemplate(ctx context.Context, tenantID, actor string, in Template) (Template, error) {
	in.SchemaVersion = firstNonempty(strings.TrimSpace(in.SchemaVersion), TemplateSchemaVersion)
	in.ID = strings.TrimSpace(in.ID)
	in.Version = strings.TrimSpace(in.Version)
	in.Name = strings.TrimSpace(in.Name)
	if report := ValidateTemplate(in); !report.Valid {
		return Template{}, fmt.Errorf("%w: %s", ErrInvalid, issueSummary(report.Issues))
	}
	if in.Parameters == nil {
		in.Parameters = map[string]any{"type": "object"}
	}
	if in.Requires == nil {
		in.Requires = map[string]string{}
	}
	in.Assembly = normalizedAssembly(in.Assembly)
	return s.store.CreateTemplate(ctx, tenantID, actor, in)
}

// DeleteTemplate removes one unused custom version. Referenced versions stay
// available for audit and rollback; the store enforces the reference fence.
func (s *Service) DeleteTemplate(ctx context.Context, tenantID, templateID, version string) error {
	templateID = strings.TrimSpace(templateID)
	version = strings.TrimSpace(version)
	if templateID == "" || version == "" {
		return fmt.Errorf("%w: template id and version are required", ErrInvalid)
	}
	return s.store.DeleteTemplate(ctx, tenantID, templateID, version)
}

// ListInstances returns every generation-fenced instance of the tenant.
func (s *Service) ListInstances(ctx context.Context, tenantID string) ([]Instance, error) {
	return s.store.ListInstances(ctx, tenantID)
}

// CreateInstance snapshots an immutable template version and persists the
// first desired generation with its deterministic expansion.
func (s *Service) CreateInstance(ctx context.Context, tenantID string, in InstanceInput) (Instance, error) {
	name := strings.TrimSpace(in.Name)
	templateID := strings.TrimSpace(in.TemplateID)
	version := strings.TrimSpace(in.TemplateVersion)
	if name == "" || templateID == "" || version == "" {
		return Instance{}, fmt.Errorf("%w: name, template_id and template_version are required", ErrInvalid)
	}
	template, err := s.store.GetTemplate(ctx, tenantID, templateID, version)
	if err != nil {
		return Instance{}, err
	}
	instance := Instance{
		ID:               uuid.NewString(),
		Name:             name,
		TemplateID:       templateID,
		TemplateVersion:  version,
		Status:           StatusDraft,
		Generation:       1,
		Parameters:       in.Parameters,
		Assembly:         in.Assembly,
		TemplateSnapshot: template,
	}
	expansion, report := Expand(template, ExpansionInput{
		InstanceID: instance.ID,
		Generation: instance.Generation,
		Parameters: instance.Parameters,
		Assembly:   instance.Assembly,
	})
	instance.Validation = report
	return s.store.CreateInstance(ctx, tenantID, instance, expansion)
}

// GetInstance loads one instance.
func (s *Service) GetInstance(ctx context.Context, tenantID, instanceID string) (Instance, error) {
	return s.store.GetInstance(ctx, tenantID, instanceID)
}

// UpdateInstance advances one desired generation only when expected_generation
// still matches. The immutable template snapshot is never re-read.
func (s *Service) UpdateInstance(ctx context.Context, tenantID, instanceID string, in InstanceUpdate) (Instance, error) {
	existing, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return Instance{}, err
	}
	if existing.Status == StatusDeploying {
		return Instance{}, fmt.Errorf("%w: instance is deploying", ErrConflict)
	}
	if in.ExpectedGeneration <= 0 || in.ExpectedGeneration != existing.Generation {
		return Instance{}, fmt.Errorf("%w: expected generation %d, current %d",
			ErrGenerationConflict, in.ExpectedGeneration, existing.Generation)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Instance{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	next := existing.Generation + 1
	expansion, report := Expand(existing.TemplateSnapshot, ExpansionInput{
		InstanceID: existing.ID,
		Generation: next,
		Parameters: in.Parameters,
		Assembly:   in.Assembly,
	})
	in.Name = name
	return s.store.UpdateInstance(ctx, tenantID, instanceID, in, expansion, report)
}

// DeleteInstance removes only an instance with no active or pending
// deployment. Deployed resources must be undeployed first.
func (s *Service) DeleteInstance(ctx context.Context, tenantID, instanceID string) error {
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return err
	}
	if instance.Status == StatusDeploying || instance.ActiveGeneration > 0 || instance.PendingGeneration > 0 {
		return fmt.Errorf("%w: undeploy the instance before deleting it", ErrConflict)
	}
	return s.store.DeleteInstance(ctx, tenantID, instanceID)
}

// ValidateInstance combines deterministic document expansion with live
// preflight and persists the authoritative report.
func (s *Service) ValidateInstance(ctx context.Context, tenantID, instanceID string) (ValidationReport, error) {
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return ValidationReport{}, err
	}
	expansion, documentReport := Expand(instance.TemplateSnapshot, ExpansionInput{
		InstanceID: instance.ID,
		Generation: instance.Generation,
		Parameters: instance.Parameters,
		Assembly:   instance.Assembly,
	})
	report := mergeReports(documentReport, s.Preflight(ctx, tenantID, instance, expansion))
	if err := s.store.SaveValidation(ctx, tenantID, instanceID, instance.Generation, report); err != nil {
		return ValidationReport{}, err
	}
	return report, nil
}

// Expansion returns one retained snapshot. Generation zero selects the current
// desired generation.
func (s *Service) Expansion(ctx context.Context, tenantID, instanceID string, generation int64) (Expansion, error) {
	if generation == 0 {
		instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
		if err != nil {
			return Expansion{}, err
		}
		generation = instance.Generation
	}
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instanceID, generation)
	if err != nil {
		return Expansion{}, err
	}
	return snapshot.Expansion, nil
}

// Preflight evaluates every owner dependency without mutating state. The
// returned report is suitable for both validation and deploy gating.
func (s *Service) Preflight(ctx context.Context, tenantID string, instance Instance, expansion Expansion) ValidationReport {
	issues := make([]Issue, 0)
	keys := make(map[string]struct{}, len(expansion.Artifacts))
	for _, artifact := range expansion.Artifacts {
		keys[artifact.Key] = struct{}{}
	}
	for _, artifact := range expansion.Artifacts {
		if artifact.Key == "" {
			issues = append(issues, blocking("invalid_artifact", "artifact key is required", "", ""))
			continue
		}
		for _, dependency := range artifact.DependsOn {
			if _, ok := keys[dependency]; !ok {
				issues = append(issues, blocking("missing_dependency",
					fmt.Sprintf("artifact depends on unknown key %q", dependency), "", artifact.Key))
			}
		}
		issues = append(issues, s.preflightArtifact(ctx, tenantID, instance, artifact)...)
	}
	sortIssues(issues)
	return ValidationReport{Valid: !hasBlocking(issues), Issues: issues}
}

func (s *Service) preflightArtifact(ctx context.Context, tenantID string, instance Instance, artifact Artifact) []Issue {
	switch artifact.Kind {
	case ArtifactKindModelDistribution:
		return s.preflightModel(ctx, tenantID, artifact)
	case ArtifactKindPipelineRegistration, ArtifactKindExternalServable:
		config, _, err := managerResourceIdentity(artifact)
		if err != nil {
			return []Issue{blocking("invalid_manager_resource", err.Error(), "", artifact.Key)}
		}
		if config["resource"] == "ExternalServablesJSON" {
			if _, managed := config["definition"]; managed {
				return []Issue{blocking("managed_external_resource_unavailable",
					"managed ExternalServablesJSON is fail-closed until NI provides a runtime-only symmetric resource; use a reference-only entry",
					"", artifact.Key)}
			}
		}
		if s.owners.Managers == nil {
			return []Issue{blocking("manager_resource_unavailable",
				"edge-manager-resource connector delivery is not enabled",
				"", artifact.Key)}
		}
		if s.owners.Devices == nil {
			return []Issue{blocking("device_owner_unavailable",
				"device registry is required to resolve the target gateway", "", artifact.Key)}
		}
		if artifact.GatewayID == "" {
			return []Issue{blocking("missing_gateway", "artifact has no target gateway", "", artifact.Key)}
		}
		if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.GatewayID); err != nil {
			return []Issue{blocking("gateway_not_found",
				fmt.Sprintf("target gateway %s is not available: %v", artifact.GatewayID, err), "", artifact.Key)}
		}
		return nil
	case ArtifactKindCompanionSubscription:
		if s.owners.Companions == nil {
			return []Issue{blocking("companion_unavailable",
				"companion subscription delivery is not signed off; the artifact cannot be deployed",
				"", artifact.Key)}
		}
		return nil
	case ArtifactKindInferenceBinding:
		return s.preflightBinding(ctx, tenantID, artifact)
	case ArtifactKindRuleChain:
		return s.preflightRule(ctx, tenantID, artifact)
	case ArtifactKindNorthboundChannel:
		return s.preflightNorthbound(ctx, tenantID, artifact)
	case ArtifactKindNorthboundFilter:
		return s.preflightNorthboundFilter(ctx, tenantID, artifact)
	default:
		return []Issue{blocking("unknown_artifact_kind",
			fmt.Sprintf("unknown expansion artifact kind %q", artifact.Kind), "", artifact.Key)}
	}
}

// preflightModel validates the declarative NH ensure request. It no longer
// gates on edge inventory READY: distribution is now requested through NH
// (nh-model-distribution/v1), and edge READY remains an observed health signal
// via reconcileHealth instead of a pre-deployment blocker.
func (s *Service) preflightModel(ctx context.Context, tenantID string, artifact Artifact) []Issue {
	if s.owners.Distribution == nil {
		return []Issue{blocking("model_distribution_unavailable",
			"NereidHorizon model distribution is not enabled; configure the horizon integration",
			"", artifact.Key)}
	}
	modelID := specString(artifact.Spec, "model_id")
	version := specString(artifact.Spec, "version")
	if modelID == "" || version == "" || artifact.GatewayID == "" {
		return []Issue{blocking("invalid_model_artifact",
			"model distribution requires model_id, version and gateway_id", "", artifact.Key)}
	}
	if err := s.owners.Distribution.CheckTarget(artifact); err != nil {
		return []Issue{blocking("model_target_invalid", err.Error(), "", artifact.Key)}
	}
	return nil
}

func (s *Service) preflightBinding(ctx context.Context, tenantID string, artifact Artifact) []Issue {
	issues := make([]Issue, 0)
	if s.owners.Connectors == nil {
		issues = append(issues, blocking("connector_owner_unavailable",
			"connector lifecycle is not enabled", "", artifact.Key))
	}
	if s.owners.Devices == nil {
		issues = append(issues, blocking("device_owner_unavailable",
			"device registry is not enabled", "", artifact.Key))
		return issues
	}
	if artifact.GatewayID == "" {
		issues = append(issues, blocking("missing_gateway",
			"inference binding has no target gateway", "", artifact.Key))
	} else if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.GatewayID); err != nil {
		issues = append(issues, blocking("gateway_not_found",
			fmt.Sprintf("target gateway %s is not available: %v", artifact.GatewayID, err), "", artifact.Key))
	}
	if artifact.DeviceID != "" {
		if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.DeviceID); err != nil {
			issues = append(issues, blocking("output_device_not_found",
				fmt.Sprintf("output virtual device %s is not available: %v", artifact.DeviceID, err), "", artifact.Key))
		}
	}
	if specMap(artifact.Spec, "config")["PipelineId"] == nil {
		issues = append(issues, blocking("missing_pipeline_id",
			"inference binding requires PipelineId", "", artifact.Key))
	}
	return issues
}

func (s *Service) preflightRule(ctx context.Context, tenantID string, artifact Artifact) []Issue {
	issues := make([]Issue, 0)
	if s.owners.Rules == nil {
		issues = append(issues, blocking("rule_owner_unavailable",
			"rule chain lifecycle is not enabled", "", artifact.Key))
	}
	graph, err := artifactGraph(artifact)
	if err != nil {
		return append(issues, blocking("invalid_rule_graph", err.Error(), "", artifact.Key))
	}
	if _, err := rule.Compile(artifact.Key, graph, rule.Deps{}); err != nil {
		issues = append(issues, blocking("invalid_rule_graph", err.Error(), "", artifact.Key))
	}
	if artifact.DeviceID != "" {
		if s.owners.Devices == nil {
			issues = append(issues, blocking("device_owner_unavailable",
				"device registry is required to bind the conclusion device", "", artifact.Key))
		} else if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.DeviceID); err != nil {
			issues = append(issues, blocking("conclusion_device_not_found",
				fmt.Sprintf("conclusion device %s is not available: %v", artifact.DeviceID, err), "", artifact.Key))
		}
	}
	return issues
}

func (s *Service) preflightNorthbound(ctx context.Context, tenantID string, artifact Artifact) []Issue {
	issues := make([]Issue, 0)
	if s.owners.Forwarders == nil {
		issues = append(issues, blocking("forwarder_owner_unavailable",
			"forwarder management is not enabled", "", artifact.Key))
	}
	forwarderID := specString(artifact.Spec, "forwarder_id")
	if forwarderID == "" {
		issues = append(issues, blocking("missing_forwarder", "northbound channel requires forwarder_id", "", artifact.Key))
	} else if s.owners.Forwarders != nil {
		if _, err := s.owners.Forwarders.Get(ctx, tenantID, forwarderID); err != nil {
			issues = append(issues, blocking("forwarder_not_found",
				fmt.Sprintf("forwarder %s is not available: %v", forwarderID, err), "", artifact.Key))
		}
	}
	if artifact.GatewayID == "" {
		issues = append(issues, blocking("missing_gateway", "northbound channel has no target gateway", "", artifact.Key))
	} else if s.owners.Devices != nil {
		if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.GatewayID); err != nil {
			issues = append(issues, blocking("gateway_not_found",
				fmt.Sprintf("target gateway %s is not available: %v", artifact.GatewayID, err), "", artifact.Key))
		}
	}
	return issues
}

func (s *Service) preflightNorthboundFilter(ctx context.Context, tenantID string, artifact Artifact) []Issue {
	issues := make([]Issue, 0)
	if s.owners.Connectors == nil {
		issues = append(issues, blocking("connector_owner_unavailable",
			"connector lifecycle is not enabled", "", artifact.Key))
	}
	if artifact.Spec["connector_type"] != "edge-northbound-filter" ||
		artifact.Spec["type_version"] != "1" || artifact.DeviceID != "" ||
		specString(artifact.Spec, "device_id") != "" {
		issues = append(issues, blocking("invalid_northbound_filter",
			"northbound filter requires edge-northbound-filter v1 with empty device_id", "", artifact.Key))
	}
	if artifact.GatewayID == "" {
		issues = append(issues, blocking("missing_gateway", "northbound filter has no target gateway", "", artifact.Key))
	} else if s.owners.Devices != nil {
		if _, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.GatewayID); err != nil {
			issues = append(issues, blocking("gateway_not_found",
				fmt.Sprintf("target gateway %s is not available: %v", artifact.GatewayID, err), "", artifact.Key))
		}
	}
	return issues
}

// Deploy validates the latest desired generation, then applies its immutable
// expansion through the destination owners. active_generation is not advanced
// until read-back reports a deployable state.
func (s *Service) Deploy(ctx context.Context, tenantID, instanceID string) (Instance, error) {
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return Instance{}, err
	}
	if instance.Status == StatusDeploying {
		return Instance{}, fmt.Errorf("%w: instance is already deploying", ErrConflict)
	}
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instanceID, instance.Generation)
	if err != nil {
		return Instance{}, err
	}
	_, documentReport := Expand(instance.TemplateSnapshot, ExpansionInput{
		InstanceID: instance.ID,
		Generation: instance.Generation,
		Parameters: instance.Parameters,
		Assembly:   instance.Assembly,
	})
	report := mergeReports(documentReport, s.Preflight(ctx, tenantID, instance, snapshot.Expansion))
	if !report.Valid {
		_ = s.store.SaveValidation(ctx, tenantID, instanceID, instance.Generation, report)
		return Instance{}, fmt.Errorf("%w: %s", ErrInvalid, issueSummary(report.Issues))
	}
	var previous *ExpansionSnapshot
	if instance.ActiveGeneration > 0 && instance.ActiveGeneration != instance.Generation {
		active, err := s.store.GetExpansion(ctx, tenantID, instanceID, instance.ActiveGeneration)
		if err != nil {
			return Instance{}, err
		}
		previous = &active
	}
	return s.deploySnapshot(ctx, tenantID, instance, snapshot.Expansion, previous)
}

// Rollback re-applies one retained generation through the same preflight and
// owner path. Resources recorded on the current active generation but absent
// from the target snapshot are removed after the target applies.
func (s *Service) Rollback(ctx context.Context, tenantID, instanceID string, generation int64) (Instance, error) {
	if generation <= 0 {
		return Instance{}, fmt.Errorf("%w: generation must be positive", ErrInvalid)
	}
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return Instance{}, err
	}
	if instance.Status == StatusDeploying {
		return Instance{}, fmt.Errorf("%w: instance is already deploying", ErrConflict)
	}
	target, err := s.store.GetExpansion(ctx, tenantID, instanceID, generation)
	if err != nil {
		return Instance{}, err
	}
	if target.Expansion.InstanceID != instance.ID {
		return Instance{}, fmt.Errorf("%w: snapshot belongs to another instance", ErrInvalid)
	}
	report := s.Preflight(ctx, tenantID, instance, target.Expansion)
	if !report.Valid {
		return Instance{}, fmt.Errorf("%w: %s", ErrInvalid, issueSummary(report.Issues))
	}
	var previous *ExpansionSnapshot
	if instance.ActiveGeneration > 0 && instance.ActiveGeneration != generation {
		active, err := s.store.GetExpansion(ctx, tenantID, instanceID, instance.ActiveGeneration)
		if err != nil {
			return Instance{}, err
		}
		previous = &active
	}
	return s.deploySnapshot(ctx, tenantID, instance, target.Expansion, previous)
}

func (s *Service) deploySnapshot(ctx context.Context, tenantID string, instance Instance, expansion Expansion, previous *ExpansionSnapshot) (Instance, error) {
	target := expansion.Generation
	if _, err := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
		Status:              StatusDeploying,
		ActiveGeneration:    nil,
		PendingGeneration:   &target,
		ExpansionGeneration: target,
		ExpansionStatus:     ExpansionDeploying,
		RequireNotDeploying: true,
	}); err != nil {
		return Instance{}, err
	}
	deploymentErr := s.applyExpansion(ctx, tenantID, instance, expansion)
	if deploymentErr == nil {
		deploymentErr = s.reconcileNorthboundFilters(ctx, tenantID, instance.ID, instance.Generation, expansion)
	}
	if deploymentErr == nil && previous != nil {
		var targetSnapshot ExpansionSnapshot
		targetSnapshot, deploymentErr = s.store.GetExpansion(ctx, tenantID, instance.ID, expansion.Generation)
		if deploymentErr == nil {
			deploymentErr = s.pruneRemoved(ctx, tenantID, *previous, targetSnapshot)
		}
	}
	if deploymentErr != nil {
		lastError := deploymentErr.Error()
		failed, updateErr := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
			Status:              StatusFailed,
			LastError:           lastError,
			PendingGeneration:   int64Pointer(0),
			ExpansionGeneration: target,
			ExpansionStatus:     ExpansionFailed,
		})
		if updateErr != nil {
			return Instance{}, fmt.Errorf("apply/prune business expansion: %w (persist failure: %v)", deploymentErr, updateErr)
		}
		return failed, deploymentErr
	}
	return s.finishDeployment(ctx, tenantID, instance, expansion)
}

func (s *Service) applyExpansion(ctx context.Context, tenantID string, instance Instance, expansion Expansion) error {
	ordered, err := orderedArtifacts(expansion)
	if err != nil {
		return err
	}
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instance.ID, expansion.Generation)
	if err != nil {
		return err
	}
	for _, artifact := range ordered {
		prior, found := snapshotResource(snapshot, artifact.Key)
		if !found {
			prior, found, err = s.store.FindResource(ctx, tenantID, instance.ID, artifact.Key)
			if err != nil {
				return err
			}
		}
		resource, err := s.applyArtifact(ctx, tenantID, instance, artifact, prior, found)
		if err != nil {
			return fmt.Errorf("apply %s: %w", artifact.Key, err)
		}
		if err := s.store.RecordResource(ctx, tenantID, instance.ID, instance.Generation, expansion, resource); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyArtifact(ctx context.Context, tenantID string, instance Instance, artifact Artifact, prior Resource, found bool) (Resource, error) {
	resource := Resource{ArtifactKey: artifact.Key, Kind: artifact.Kind}
	switch artifact.Kind {
	case ArtifactKindModelDistribution:
		// Declarative ensure through NereidHorizon
		// (nh-model-distribution/v1). NL never moves model bytes: NH streams
		// the Forge package through ESS and owns idempotency, task
		// lifecycle, cancellation and rollback. The NH task id is the
		// durable resource handle; edge READY stays an observed health
		// signal, not a synchronous apply condition.
		if s.owners.Distribution == nil {
			return Resource{}, fmt.Errorf("%w: model distribution (NereidHorizon) is not enabled", ErrInvalid)
		}
		handle, err := s.owners.Distribution.EnsureDistribution(ctx, tenantID, artifact)
		if err != nil {
			return Resource{}, err
		}
		resource.OwnerID = specString(artifact.Spec, "model_id") + "@" + specString(artifact.Spec, "version")
		resource.ResourceID = handle.TaskID
		return resource, nil
	case ArtifactKindPipelineRegistration, ArtifactKindExternalServable:
		if s.owners.Managers == nil {
			return Resource{}, fmt.Errorf("%w: manager-resource delivery is not available", ErrInvalid)
		}
		_, identity, err := managerResourceIdentity(artifact)
		if err != nil {
			return Resource{}, err
		}
		resource.OwnerID = identity
		// NE forbids changing manager/resource/entry identity in place. Keep
		// the old resource for reverse-dependency pruning after target apply.
		if found && prior.OwnerID != "" && prior.OwnerID != identity {
			found = false
		}
		if !found || prior.ResourceID == "" {
			live, liveFound, err := s.store.FindLiveResource(ctx, tenantID, artifact.Kind, identity)
			if err != nil {
				return Resource{}, err
			}
			if liveFound && live.ResourceID != "" {
				if live.InstanceID == instance.ID {
					prior, found = live.Resource, true
				} else {
					existing, err := s.owners.Managers.GetSpec(ctx, tenantID, live.ResourceID)
					if err != nil {
						return Resource{}, err
					}
					input, err := connectorSaveInput(artifact)
					if err != nil {
						return Resource{}, err
					}
					if !sameJSONDocument(existing.Config, input.Config) {
						return Resource{}, fmt.Errorf("%w: manager identity is live with a different definition", ErrConflict)
					}
					// Identical manager entries share one spec. Live-reference pruning
					// deletes it only after the final active/pending reference leaves.
					resource.ResourceID = live.ResourceID
					return resource, nil
				}
			}
		}
		return s.applyConnectorSpec(ctx, tenantID, artifact, prior, found, resource, s.owners.Managers)
	case ArtifactKindCompanionSubscription:
		if s.owners.Companions == nil {
			return Resource{}, fmt.Errorf("%w: companion subscription delivery is not available", ErrInvalid)
		}
		id, err := s.owners.Companions.ApplySubscription(ctx, tenantID, artifact)
		if err != nil {
			return Resource{}, err
		}
		resource.ResourceID = id
		return resource, nil
	case ArtifactKindInferenceBinding:
		if s.owners.Connectors == nil {
			return Resource{}, fmt.Errorf("%w: connector lifecycle is not available", ErrInvalid)
		}
		return s.applyConnectorSpec(ctx, tenantID, artifact, prior, found, resource, s.owners.Connectors)
	case ArtifactKindRuleChain:
		if s.owners.Rules == nil {
			return Resource{}, fmt.Errorf("%w: rule lifecycle is not available", ErrInvalid)
		}
		return s.applyRuleChain(ctx, tenantID, instance, artifact, prior, found)
	case ArtifactKindNorthboundChannel:
		// The forwarder is a shared, pre-existing NL resource. Record the
		// external reference so audit shows the target without transferring
		// ownership or deleting it on undeploy.
		resource.OwnerID = specString(artifact.Spec, "forwarder_id")
		return resource, nil
	case ArtifactKindNorthboundFilter:
		// The concrete connector is reconciled after the whole target
		// expansion so its policy is the live union across every instance.
		// Retain the known connector id across a same-generation retry;
		// reconciliation replaces it if the owner reports the spec missing.
		resource.ResourceID = prior.ResourceID
		resource.OwnerID = northboundFilterIdentity(artifact)
		return resource, nil
	default:
		return Resource{}, fmt.Errorf("%w: unknown artifact kind %q", ErrInvalid, artifact.Kind)
	}
}

func (s *Service) applyConnectorSpec(
	ctx context.Context,
	tenantID string,
	artifact Artifact,
	prior Resource,
	found bool,
	resource Resource,
	owner ConnectorOwner,
) (Resource, error) {
	input, err := connectorSaveInput(artifact)
	if err != nil {
		return Resource{}, err
	}
	if found && prior.ResourceID != "" {
		updated, err := owner.UpdateSpec(ctx, tenantID, prior.ResourceID, input)
		if err == nil {
			resource.ResourceID = updated.ID
			return resource, nil
		}
		if !errors.Is(err, connector.ErrNotFound) {
			return Resource{}, err
		}
	}
	created, err := owner.CreateSpec(ctx, tenantID, artifact.GatewayID, input)
	if err != nil {
		return Resource{}, err
	}
	resource.ResourceID = created.ID
	return resource, nil
}

func (s *Service) applyRuleChain(ctx context.Context, tenantID string, instance Instance, artifact Artifact, prior Resource, found bool) (Resource, error) {
	graph, err := artifactGraph(artifact)
	if err != nil {
		return Resource{}, err
	}
	groupID := ""
	previousID := ""
	if found {
		groupID = prior.OwnerID
		previousID = prior.PreviousID
	}
	if groupID == "" {
		groupID, err = s.owners.Rules.CreateGroup(ctx, tenantID, ruleGroupName(instance, artifact))
		if err != nil {
			return Resource{}, err
		}
	}
	chainID, _, err := s.owners.Rules.CreateDraft(ctx, tenantID, groupID, graph)
	if err != nil {
		return Resource{}, err
	}
	if err := s.owners.Rules.Activate(ctx, tenantID, chainID); err != nil {
		return Resource{}, err
	}
	if artifact.DeviceID != "" {
		if s.owners.Devices == nil {
			return Resource{}, fmt.Errorf("%w: device registry is not available", ErrInvalid)
		}
		device, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.DeviceID)
		if err != nil {
			return Resource{}, err
		}
		if previousID == "" && device.RuleChainGroupID != groupID {
			previousID = device.RuleChainGroupID
		}
		device.RuleChainGroupID = groupID
		if _, err := s.owners.Devices.UpdateDevice(ctx, tenantID, artifact.DeviceID, device); err != nil {
			return Resource{}, err
		}
	}
	return Resource{
		ArtifactKey: artifact.Key,
		Kind:        artifact.Kind,
		ResourceID:  chainID,
		OwnerID:     groupID,
		PreviousID:  previousID,
	}, nil
}

func (s *Service) reconcileNorthboundFilters(
	ctx context.Context, tenantID, currentInstanceID string, desiredGeneration int64, target Expansion,
) error {
	live, err := s.store.ListLiveExpansions(ctx, tenantID)
	if err != nil {
		return err
	}
	channelsByGateway := map[string][]Artifact{}
	for _, snapshot := range live {
		if snapshot.InstanceID == currentInstanceID {
			continue
		}
		for _, artifact := range snapshot.Expansion.Artifacts {
			if artifact.Kind == ArtifactKindNorthboundChannel && artifact.GatewayID != "" {
				channelsByGateway[artifact.GatewayID] = append(channelsByGateway[artifact.GatewayID], artifact)
			}
		}
	}
	for _, artifact := range target.Artifacts {
		if artifact.Kind == ArtifactKindNorthboundChannel && artifact.GatewayID != "" {
			channelsByGateway[artifact.GatewayID] = append(channelsByGateway[artifact.GatewayID], artifact)
		}
	}
	existing, err := s.store.ListLiveResources(ctx, tenantID, ArtifactKindNorthboundFilter)
	if err != nil {
		return err
	}
	existingByIdentity := map[string]LiveResource{}
	for _, resource := range existing {
		if resource.ResourceID == "" {
			continue
		}
		if _, exists := existingByIdentity[resource.OwnerID]; !exists {
			existingByIdentity[resource.OwnerID] = resource
		}
	}
	gateways := make([]string, 0, len(channelsByGateway))
	for gatewayID := range channelsByGateway {
		gateways = append(gateways, gatewayID)
	}
	sort.Strings(gateways)
	desired := map[string]struct{}{}
	for _, gatewayID := range gateways {
		desired[gatewayID] = struct{}{}
		resource, err := s.applyNorthboundFilter(ctx, tenantID, gatewayID,
			northboundFilterConfig(channelsByGateway[gatewayID]), existingByIdentity)
		if err != nil {
			return err
		}
		if artifact, ok := findArtifactByKey(target, northboundFilterKey(gatewayID)); ok {
			resource.ArtifactKey = artifact.Key
			resource.Kind = artifact.Kind
			if err := s.store.RecordResource(ctx, tenantID, currentInstanceID, desiredGeneration, target, resource); err != nil {
				return err
			}
		}
	}
	for identity, resource := range existingByIdentity {
		gatewayID := northboundFilterGateway(identity)
		if gatewayID == "" {
			continue
		}
		if _, keep := desired[gatewayID]; keep {
			continue
		}
		if err := s.quiesceNorthboundFilter(ctx, tenantID, gatewayID, resource); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyNorthboundFilter(
	ctx context.Context, tenantID, gatewayID string, config map[string]any,
	existing map[string]LiveResource,
) (Resource, error) {
	if s.owners.Connectors == nil {
		return Resource{}, fmt.Errorf("%w: connector lifecycle is not available", ErrInvalid)
	}
	identity := northboundFilterIdentityFromGateway(gatewayID)
	input, err := northboundFilterSaveInput(config)
	if err != nil {
		return Resource{}, err
	}
	if live, ok := existing[identity]; ok && live.ResourceID != "" {
		current, err := s.owners.Connectors.GetSpec(ctx, tenantID, live.ResourceID)
		if err == nil {
			if sameJSONDocument(current.Config, input.Config) {
				return Resource{ResourceID: current.ID, OwnerID: identity}, nil
			}
			updated, err := s.owners.Connectors.UpdateSpec(ctx, tenantID, live.ResourceID, input)
			if err != nil {
				return Resource{}, err
			}
			return Resource{ResourceID: updated.ID, OwnerID: identity}, nil
		} else if !errors.Is(err, connector.ErrNotFound) {
			return Resource{}, err
		}
	}
	created, err := s.owners.Connectors.CreateSpec(ctx, tenantID, gatewayID, input)
	if err != nil {
		return Resource{}, err
	}
	return Resource{ResourceID: created.ID, OwnerID: identity}, nil
}

func (s *Service) quiesceNorthboundFilter(ctx context.Context, tenantID, gatewayID string, live LiveResource) error {
	if s.owners.Connectors == nil || live.ResourceID == "" {
		return nil
	}
	input, err := northboundFilterSaveInput(quiescentNorthboundFilterConfig())
	if err != nil {
		return err
	}
	current, err := s.owners.Connectors.GetSpec(ctx, tenantID, live.ResourceID)
	if errors.Is(err, connector.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sameJSONDocument(current.Config, input.Config) {
		return nil
	}
	_, err = s.owners.Connectors.UpdateSpec(ctx, tenantID, live.ResourceID, input)
	return err
}

func northboundFilterSaveInput(config map[string]any) (connector.SaveInput, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return connector.SaveInput{}, fmt.Errorf("encode northbound filter config: %w", err)
	}
	return connector.SaveInput{
		ConnectorType: "edge-northbound-filter",
		TypeVersion:   "1",
		Config:        raw,
		DeviceID:      "",
	}, nil
}

func northboundFilterIdentity(artifact Artifact) string {
	return northboundFilterIdentityFromGateway(artifact.GatewayID)
}

func northboundFilterIdentityFromGateway(gatewayID string) string {
	return strings.Join([]string{gatewayID, "edge-northbound-filter", "1"}, "|")
}

func northboundFilterGateway(identity string) string {
	parts := strings.Split(identity, "|")
	if len(parts) != 3 || parts[1] != "edge-northbound-filter" || parts[2] != "1" {
		return ""
	}
	return parts[0]
}

func findArtifactByKey(expansion Expansion, key string) (Artifact, bool) {
	for _, artifact := range expansion.Artifacts {
		if artifact.Key == key {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func (s *Service) pruneRemoved(ctx context.Context, tenantID string, previous, target ExpansionSnapshot) error {
	targetResources := make(map[string]Resource, len(target.Resources))
	for _, resource := range target.Resources {
		targetResources[resource.ArtifactKey] = resource
	}
	ordered, err := orderedArtifacts(previous.Expansion)
	if err != nil {
		return err
	}
	resources := make(map[string]Resource, len(previous.Resources))
	for _, resource := range previous.Resources {
		resources[resource.ArtifactKey] = resource
	}
	for index := len(ordered) - 1; index >= 0; index-- {
		artifact := ordered[index]
		resource, ok := resources[artifact.Key]
		if !ok {
			continue
		}
		if targetResource, keep := targetResources[resource.ArtifactKey]; keep && sameOwnerResource(resource, targetResource) {
			continue
		}
		referenced, err := s.store.ResourceReferenced(ctx, tenantID, resource,
			previous.Expansion.InstanceID, previous.Expansion.Generation)
		if err != nil {
			return fmt.Errorf("check live references for %s: %w", resource.ArtifactKey, err)
		}
		if referenced {
			continue
		}
		if err := s.removeResource(ctx, tenantID, artifact, resource); err != nil {
			return fmt.Errorf("prune %s: %w", resource.ArtifactKey, err)
		}
	}
	return nil
}

func sameOwnerResource(previous, target Resource) bool {
	if previous.Kind != target.Kind {
		return false
	}
	if previous.Kind == ArtifactKindRuleChain {
		return previous.OwnerID != "" && previous.OwnerID == target.OwnerID
	}
	if previous.ResourceID == "" || target.ResourceID == "" {
		return previous.ResourceID == target.ResourceID && previous.OwnerID == target.OwnerID
	}
	return previous.ResourceID == target.ResourceID
}

// Undeploy removes only resources recorded for the active snapshot. Shared
// models, pipeline registrations, companions, and forwarders are untouched.
func (s *Service) Undeploy(ctx context.Context, tenantID, instanceID string) (Instance, error) {
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return Instance{}, err
	}
	if instance.Status == StatusDeploying {
		return Instance{}, fmt.Errorf("%w: instance is deploying", ErrConflict)
	}
	generation := instance.ActiveGeneration
	if generation == 0 {
		generation = instance.PendingGeneration
	}
	if generation == 0 {
		generation = instance.Generation
	}
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instanceID, generation)
	if err != nil {
		return Instance{}, err
	}
	ordered, err := orderedArtifacts(snapshot.Expansion)
	if err != nil {
		return Instance{}, err
	}
	resources := make(map[string]Resource, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		resources[resource.ArtifactKey] = resource
	}
	for index := len(ordered) - 1; index >= 0; index-- {
		artifact := ordered[index]
		resource, ok := resources[artifact.Key]
		if !ok {
			continue
		}
		referenced, err := s.store.ResourceReferenced(ctx, tenantID, resource, instanceID, generation)
		if err != nil {
			return s.failUndeploy(ctx, tenantID, instance, generation, err)
		}
		if referenced {
			continue
		}
		if err := s.removeResource(ctx, tenantID, artifact, resource); err != nil {
			return s.failUndeploy(ctx, tenantID, instance, generation, err)
		}
	}
	if err := s.reconcileNorthboundFilters(ctx, tenantID, instanceID, instance.Generation, Expansion{
		Artifacts: []Artifact{},
	}); err != nil {
		return s.failUndeploy(ctx, tenantID, instance, generation, err)
	}
	zero := int64(0)
	updated, err := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
		Status:              StatusUndeployed,
		ActiveGeneration:    &zero,
		PendingGeneration:   &zero,
		ExpansionGeneration: generation,
		ExpansionStatus:     ExpansionUndeployed,
		RequireNotDeploying: true,
	})
	if err != nil {
		return Instance{}, err
	}
	return updated, nil
}

func (s *Service) failUndeploy(ctx context.Context, tenantID string, instance Instance, generation int64, cleanupErr error) (Instance, error) {
	failed, updateErr := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
		Status: StatusFailed, LastError: cleanupErr.Error(),
		ExpansionGeneration: generation, ExpansionStatus: ExpansionFailed,
		RequireNotDeploying: true,
	})
	if updateErr != nil {
		return Instance{}, fmt.Errorf("undeploy cleanup: %w (persist failure: %v)", cleanupErr, updateErr)
	}
	return failed, cleanupErr
}

func (s *Service) removeResource(ctx context.Context, tenantID string, artifact Artifact, resource Resource) error {
	switch resource.Kind {
	case ArtifactKindInferenceBinding:
		if resource.ResourceID == "" {
			return nil
		}
		if s.owners.Connectors == nil {
			return fmt.Errorf("connector owner is unavailable for %s", resource.ArtifactKey)
		}
		if err := s.owners.Connectors.DeleteSpec(ctx, tenantID, resource.ResourceID); err != nil &&
			!errors.Is(err, connector.ErrNotFound) {
			return err
		}
	case ArtifactKindRuleChain:
		if resource.OwnerID == "" {
			return nil
		}
		if s.owners.Rules == nil {
			return fmt.Errorf("rule owner is unavailable for %s", resource.ArtifactKey)
		}
		if artifact.DeviceID != "" {
			if s.owners.Devices == nil {
				return fmt.Errorf("device owner is unavailable for %s", resource.ArtifactKey)
			}
			device, err := s.owners.Devices.GetDevice(ctx, tenantID, artifact.DeviceID)
			if err != nil {
				return fmt.Errorf("restore conclusion device %s: %w", artifact.DeviceID, err)
			}
			device.RuleChainGroupID = resource.PreviousID
			if _, err := s.owners.Devices.UpdateDevice(ctx, tenantID, artifact.DeviceID, device); err != nil {
				return fmt.Errorf("restore conclusion device %s: %w", artifact.DeviceID, err)
			}
		}
		if err := s.owners.Rules.SetGroupEnabled(ctx, tenantID, resource.OwnerID, false); err != nil &&
			!errors.Is(err, rule.ErrGroupNotFound) {
			return err
		}
		if err := s.owners.Rules.DeleteGroup(ctx, tenantID, resource.OwnerID); err != nil &&
			!errors.Is(err, rule.ErrGroupNotFound) {
			return err
		}
	case ArtifactKindPipelineRegistration, ArtifactKindExternalServable:
		if resource.ResourceID == "" {
			return nil
		}
		if s.owners.Managers == nil {
			return fmt.Errorf("manager-resource owner is unavailable for %s", resource.ArtifactKey)
		}
		if err := s.owners.Managers.DeleteSpec(ctx, tenantID, resource.ResourceID); err != nil &&
			!errors.Is(err, connector.ErrNotFound) {
			return err
		}
	case ArtifactKindCompanionSubscription:
		if resource.ResourceID == "" {
			return nil
		}
		if s.owners.Companions == nil {
			return fmt.Errorf("companion owner is unavailable for %s", resource.ArtifactKey)
		}
		return s.owners.Companions.RemoveSubscription(ctx, tenantID, resource)
	case ArtifactKindModelDistribution, ArtifactKindNorthboundChannel:
		// Never unload a NI servable or delete a shared forwarder.
	case ArtifactKindNorthboundFilter:
		// The singleton is quiesced or updated by cross-instance reconciliation.
	default:
		return fmt.Errorf("unknown owned resource kind %q", resource.Kind)
	}
	return nil
}

func (s *Service) finishDeployment(ctx context.Context, tenantID string, instance Instance, expansion Expansion) (Instance, error) {
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instance.ID, expansion.Generation)
	if err != nil {
		return Instance{}, err
	}
	status, _, issues := s.readHealth(ctx, tenantID, expansion, snapshot)
	target := expansion.Generation
	patch := DeploymentPatch{
		Status:              status,
		PendingGeneration:   int64Pointer(0),
		ExpansionGeneration: target,
		ExpansionStatus:     ExpansionActive,
	}
	switch status {
	case StatusRunning, StatusDegraded:
		patch.ActiveGeneration = &target
	case StatusFailed:
		patch.ActiveGeneration = nil
		patch.ExpansionStatus = ExpansionFailed
		patch.LastError = issueSummary(issues)
	case StatusDeploying:
		patch.PendingGeneration = &target
		patch.ExpansionStatus = ExpansionDeploying
	}
	return s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, patch)
}

// Monitor performs health read-back on demand. A pending deployment is
// promoted to active only after the read-back says every required owner is
// healthy or degraded; missing job metrics stay unavailable.
func (s *Service) Monitor(ctx context.Context, tenantID, instanceID string) (Monitor, error) {
	instance, err := s.store.GetInstance(ctx, tenantID, instanceID)
	if err != nil {
		return Monitor{}, err
	}
	generation := instance.ActiveGeneration
	if instance.PendingGeneration > 0 {
		generation = instance.PendingGeneration
	}
	if generation == 0 {
		generation = instance.Generation
	}
	snapshot, err := s.store.GetExpansion(ctx, tenantID, instanceID, generation)
	if err != nil {
		return Monitor{}, err
	}
	status, cameras, issues := s.readHealth(ctx, tenantID, snapshot.Expansion, snapshot)
	if instance.Status == StatusDeploying && instance.PendingGeneration > 0 {
		target := instance.PendingGeneration
		patch := DeploymentPatch{
			Status:              status,
			PendingGeneration:   int64Pointer(0),
			ExpansionGeneration: target,
			ExpansionStatus:     ExpansionActive,
		}
		switch status {
		case StatusRunning, StatusDegraded:
			patch.ActiveGeneration = &target
		case StatusFailed:
			patch.ActiveGeneration = nil
			patch.ExpansionStatus = ExpansionFailed
			patch.LastError = issueSummary(issues)
		default:
			patch.PendingGeneration = &target
			patch.ExpansionStatus = ExpansionDeploying
			status = StatusDeploying
		}
		updated, updateErr := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, patch)
		if updateErr != nil {
			return Monitor{}, updateErr
		}
		instance = updated
	} else if status == StatusDegraded && instance.Status == StatusRunning {
		updated, updateErr := s.store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
			Status: StatusDegraded,
		})
		if updateErr == nil {
			instance = updated
		}
	}
	if instance.Status == StatusUndeployed {
		status = StatusUndeployed
	}
	return Monitor{
		InstanceID:       instance.ID,
		Generation:       snapshot.Expansion.Generation,
		ActiveGeneration: instance.ActiveGeneration,
		Status:           status,
		Cameras:          cameras,
		Issues:           issues,
	}, nil
}

func (s *Service) readHealth(ctx context.Context, tenantID string, expansion Expansion, snapshot ExpansionSnapshot) (string, []CameraMonitor, []Issue) {
	resources := make(map[string]Resource, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		resources[resource.ArtifactKey] = resource
	}
	cameras := make([]CameraMonitor, 0)
	issues := make([]Issue, 0)
	sawPending := false
	sawDegraded := false
	sawFailed := false
	for _, artifact := range expansion.Artifacts {
		switch artifact.Kind {
		case ArtifactKindInferenceBinding:
			monitor := CameraMonitor{
				NodeID:       cameraNodeID(artifact.Key),
				DeviceID:     artifact.DeviceID,
				GatewayID:    artifact.GatewayID,
				State:        "PENDING",
				HealthSource: "connector",
			}
			resource, ok := resources[artifact.Key]
			if !ok || resource.ResourceID == "" {
				sawPending = true
				monitor.LastError = "connector resource is not recorded"
				issues = append(issues, healthIssue("connector_missing",
					"generated connector has not been applied", artifact.Key))
				cameras = append(cameras, monitor)
				continue
			}
			if s.owners.Connectors == nil {
				monitor.LastError = "connector lifecycle is unavailable"
				cameras = append(cameras, monitor)
				continue
			}
			spec, err := s.owners.Connectors.GetSpec(ctx, tenantID, resource.ResourceID)
			if err != nil {
				sawPending = true
				monitor.LastError = err.Error()
				issues = append(issues, healthIssue("connector_missing",
					fmt.Sprintf("read generated connector: %v", err), artifact.Key))
				cameras = append(cameras, monitor)
				continue
			}
			monitor.State = spec.State
			monitor.LastError = spec.ReportReason
			switch spec.State {
			case "APPLIED":
			case "DEGRADED":
				sawDegraded = true
			case "FAILED":
				sawFailed = true
			default:
				sawPending = true
			}
			if s.owners.Metrics != nil {
				if metrics, available, err := s.owners.Metrics.JobMetrics(ctx, tenantID, resource.ResourceID); err == nil && available {
					monitor.SuccessTotal = metrics.SuccessTotal
					monitor.ErrorTotal = metrics.ErrorTotal
					monitor.P50MS = metrics.P50MS
					monitor.P95MS = metrics.P95MS
					monitor.EventsPerSecond = metrics.EventsPerSecond
					monitor.HealthSource = "connector+job_metrics"
				}
			}
			cameras = append(cameras, monitor)
		case ArtifactKindNorthboundFilter:
			resource, ok := resources[artifact.Key]
			if !ok || resource.ResourceID == "" {
				sawPending = true
				issues = append(issues, healthIssue("northbound_filter_missing",
					"gateway northbound filter has not been applied", artifact.Key))
				continue
			}
			if s.owners.Connectors == nil {
				sawFailed = true
				issues = append(issues, healthIssue("connector_owner_unavailable",
					"connector lifecycle is unavailable", artifact.Key))
				continue
			}
			spec, err := s.owners.Connectors.GetSpec(ctx, tenantID, resource.ResourceID)
			if err != nil {
				sawPending = true
				issues = append(issues, healthIssue("northbound_filter_missing",
					fmt.Sprintf("read northbound filter connector: %v", err), artifact.Key))
				continue
			}
			switch spec.State {
			case "APPLIED":
			case "DEGRADED":
				sawDegraded = true
				issues = append(issues, healthIssue("northbound_filter_degraded",
					"northbound filter degraded: "+spec.ReportReason, artifact.Key))
			case "FAILED":
				sawFailed = true
				issues = append(issues, healthIssue("northbound_filter_failed",
					"northbound filter failed: "+spec.ReportReason, artifact.Key))
			default:
				sawPending = true
			}
		case ArtifactKindPipelineRegistration, ArtifactKindExternalServable:
			resource, ok := resources[artifact.Key]
			if !ok || resource.ResourceID == "" {
				sawPending = true
				issues = append(issues, healthIssue("manager_resource_missing",
					"manager connector has not been applied", artifact.Key))
				continue
			}
			if s.owners.Managers == nil {
				sawFailed = true
				issues = append(issues, healthIssue("manager_resource_unavailable",
					"manager connector lifecycle is unavailable", artifact.Key))
				continue
			}
			spec, err := s.owners.Managers.GetSpec(ctx, tenantID, resource.ResourceID)
			if err != nil {
				sawPending = true
				issues = append(issues, healthIssue("manager_resource_missing",
					fmt.Sprintf("read manager connector: %v", err), artifact.Key))
				continue
			}
			switch spec.State {
			case "APPLIED":
				switch {
				case spec.ReportedRevision != spec.Revision:
					sawPending = true
					issues = append(issues, healthIssue("manager_revision_pending",
						fmt.Sprintf("manager reported revision %d, want %d", spec.ReportedRevision, spec.Revision), artifact.Key))
				case spec.ObservedRevision == nil || *spec.ObservedRevision != spec.Revision || spec.ObservedHash == "":
					sawPending = true
					issues = append(issues, healthIssue("manager_readback_pending",
						"manager APPLIED lacks current observed_revision and observed_hash", artifact.Key))
				}
			case "DEGRADED":
				sawDegraded = true
				code := "manager_degraded"
				if spec.ReportReason == "manager-entry-missing" {
					code = "manager-entry-missing"
				}
				issues = append(issues, healthIssue(code,
					"manager resource degraded: "+spec.ReportReason, artifact.Key))
			case "FAILED":
				sawFailed = true
				issues = append(issues, healthIssue("manager_failed",
					"manager resource failed: "+spec.ReportReason, artifact.Key))
			default:
				sawPending = true
			}
		case ArtifactKindModelDistribution:
			if s.owners.Models == nil {
				continue
			}
			modelID := specString(artifact.Spec, "model_id")
			version := specString(artifact.Spec, "version")
			models, err := s.owners.Models.ListForGateway(ctx, tenantID, artifact.GatewayID)
			if err != nil {
				sawDegraded = true
				issues = append(issues, healthIssue("model_inventory_failed", err.Error(), artifact.Key))
				continue
			}
			ready := false
			for _, model := range models {
				if model.ModelID == modelID && model.Version == version &&
					model.State == modelinventory.StateReady && !model.Stale {
					ready = true
					break
				}
			}
			if !ready {
				sawDegraded = true
				issues = append(issues, healthIssue("model_not_ready",
					fmt.Sprintf("model %s/%s is no longer READY on %s", modelID, version, artifact.GatewayID),
					artifact.Key))
			}
		}
	}
	switch {
	case sawFailed:
		return StatusFailed, cameras, issues
	case sawDegraded:
		return StatusDegraded, cameras, issues
	case sawPending:
		return StatusDeploying, cameras, issues
	default:
		return StatusRunning, cameras, issues
	}
}

// managerResourceIdentity validates the closed v1 envelope and returns its
// stable sharing identity. Definition content/version deliberately does not
// participate: a version update must retain the same connector spec id.
func managerResourceIdentity(artifact Artifact) (map[string]any, string, error) {
	if artifact.Spec["connector_type"] != "edge-manager-resource" ||
		artifact.Spec["type_version"] != "1" || artifact.DeviceID != "" ||
		specString(artifact.Spec, "device_id") != "" {
		return nil, "", fmt.Errorf("manager resource requires edge-manager-resource v1 with empty device_id")
	}
	config := specMap(artifact.Spec, "config")
	managerDevice := specString(config, "manager_device")
	resource := specString(config, "resource")
	if managerDevice == "" || resource == "" {
		return nil, "", fmt.Errorf("manager_device and resource are required")
	}
	pipelineID := specString(config, "pipeline_id")
	entryID := specString(config, "entry_id")
	if (pipelineID == "") == (entryID == "") {
		return nil, "", fmt.Errorf("exactly one of pipeline_id or entry_id+id_field is required")
	}
	idField := specString(config, "id_field")
	if pipelineID != "" {
		entryID = pipelineID
		if idField == "" {
			idField = "pipelineId"
		}
		if idField != "pipelineId" {
			return nil, "", fmt.Errorf("pipeline_id only permits id_field=pipelineId")
		}
	} else if idField == "" {
		return nil, "", fmt.Errorf("generic manager identity requires id_field")
	}
	identity := strings.Join([]string{artifact.GatewayID, managerDevice, resource, idField, entryID}, "|")
	return config, identity, nil
}

func sameJSONDocument(left, right json.RawMessage) bool {
	decode := func(raw json.RawMessage) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	leftCanonical, err := decode(left)
	if err != nil {
		return false
	}
	rightCanonical, err := decode(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}

func connectorSaveInput(artifact Artifact) (connector.SaveInput, error) {
	config := specMap(artifact.Spec, "config")
	raw, err := json.Marshal(config)
	if err != nil {
		return connector.SaveInput{}, fmt.Errorf("encode connector config: %w", err)
	}
	return connector.SaveInput{
		ConnectorType: specString(artifact.Spec, "connector_type"),
		TypeVersion:   specString(artifact.Spec, "type_version"),
		Config:        raw,
		DeviceID:      firstNonempty(specString(artifact.Spec, "device_id"), artifact.DeviceID),
	}, nil
}

func artifactGraph(artifact Artifact) (rule.GraphDTO, error) {
	raw, err := json.Marshal(artifact.Spec["graph"])
	if err != nil {
		return rule.GraphDTO{}, fmt.Errorf("encode rule graph: %w", err)
	}
	var graph rule.GraphDTO
	if err := json.Unmarshal(raw, &graph); err != nil {
		return rule.GraphDTO{}, fmt.Errorf("decode rule graph: %w", err)
	}
	if graph.Nodes == nil {
		graph.Nodes = []rule.NodeDTO{}
	}
	if graph.Edges == nil {
		graph.Edges = []rule.EdgeDTO{}
	}
	return graph, nil
}

func orderedArtifacts(expansion Expansion) ([]Artifact, error) {
	byKey := make(map[string]Artifact, len(expansion.Artifacts))
	indegree := make(map[string]int, len(expansion.Artifacts))
	dependents := make(map[string][]string, len(expansion.Artifacts))
	for _, artifact := range expansion.Artifacts {
		if artifact.Key == "" {
			return nil, fmt.Errorf("%w: artifact key is required", ErrInvalid)
		}
		if _, exists := byKey[artifact.Key]; exists {
			return nil, fmt.Errorf("%w: duplicate artifact key %s", ErrInvalid, artifact.Key)
		}
		byKey[artifact.Key] = artifact
		indegree[artifact.Key] = 0
	}
	for key, artifact := range byKey {
		for _, dependency := range artifact.DependsOn {
			if _, exists := byKey[dependency]; !exists {
				return nil, fmt.Errorf("%w: artifact %s depends on unknown %s", ErrInvalid, key, dependency)
			}
			indegree[key]++
			dependents[dependency] = append(dependents[dependency], key)
		}
	}
	ready := make([]string, 0)
	for key, degree := range indegree {
		if degree == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	ordered := make([]Artifact, 0, len(byKey))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byKey[key])
		for _, dependent := range dependents[key] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(byKey) {
		return nil, fmt.Errorf("%w: artifact dependency cycle", ErrInvalid)
	}
	return ordered, nil
}

func snapshotResource(snapshot ExpansionSnapshot, key string) (Resource, bool) {
	for _, resource := range snapshot.Resources {
		if resource.ArtifactKey == key {
			return resource, true
		}
	}
	return Resource{}, false
}

func ruleGroupName(instance Instance, artifact Artifact) string {
	label := artifact.Key
	if name := specString(artifact.Spec, "name"); name != "" {
		label = name
	}
	suffix := instance.ID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("business:%s:%s", label, suffix)
}

func cameraNodeID(artifactKey string) string {
	parts := strings.SplitN(artifactKey, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return artifactKey
}

func specMap(spec map[string]any, key string) map[string]any {
	if spec == nil {
		return map[string]any{}
	}
	if object, ok := spec[key].(map[string]any); ok {
		return object
	}
	if object, ok := spec[key].(map[string]string); ok {
		converted := make(map[string]any, len(object))
		for name, value := range object {
			converted[name] = value
		}
		return converted
	}
	return map[string]any{}
}

func specString(spec map[string]any, key string) string {
	if spec == nil {
		return ""
	}
	value, _ := spec[key].(string)
	return strings.TrimSpace(value)
}

func specStrings(spec map[string]any, key string) []string {
	if spec == nil {
		return []string{}
	}
	values, ok := spec[key].([]string)
	if !ok {
		return []string{}
	}
	return append([]string(nil), values...)
}

func mergeReports(reports ...ValidationReport) ValidationReport {
	merged := ValidationReport{Valid: true, Issues: make([]Issue, 0)}
	for _, report := range reports {
		if !report.Valid {
			merged.Valid = false
		}
		merged.Issues = append(merged.Issues, report.Issues...)
	}
	sortIssues(merged.Issues)
	return merged
}

func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left, right := issues[i], issues[j]
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

func hasBlocking(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Blocking {
			return true
		}
	}
	return false
}

func blocking(code, message, nodeID, artifactKey string) Issue {
	return Issue{Code: code, Message: message, NodeID: nodeID, ArtifactKey: artifactKey, Blocking: true}
}

func healthIssue(code, message, artifactKey string) Issue {
	return Issue{Code: code, Message: message, ArtifactKey: artifactKey, Blocking: false}
}

func issueSummary(issues []Issue) string {
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Blocking {
			messages = append(messages, issue.Code+": "+issue.Message)
		}
	}
	if len(messages) == 0 {
		for _, issue := range issues {
			messages = append(messages, issue.Code+": "+issue.Message)
		}
	}
	return strings.Join(messages, "; ")
}

func int64Pointer(value int64) *int64 { return &value }
