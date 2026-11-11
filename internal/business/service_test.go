// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/egress"
	"github.com/nereidai/nereidlink/internal/modelinventory"
	"github.com/nereidai/nereidlink/internal/registry"
	"github.com/nereidai/nereidlink/internal/rule"
)

func TestPreflightBlocksUnsignedManagerAndCompanionDelivery(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{
		Connectors:   &fakeConnectors{state: "APPLIED"},
		Rules:        &fakeRules{},
		Devices:      &fakeDevices{},
		Forwarders:   &fakeForwarders{},
		Models:       &fakeModels{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)
	report, err := service.ValidateInstance(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !hasIssue(report, "manager_resource_unavailable") ||
		!hasIssue(report, "companion_unavailable") {
		t.Fatalf("expected gated capabilities to block preflight: %+v", report)
	}
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deploy must fail before side effects, got %v", err)
	}
	if service.owners.Connectors.(*fakeConnectors).creates != 0 {
		t.Fatal("blocked preflight created a connector")
	}
}

func TestDeployPromotesActiveOnlyAfterHealthyReadBack(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	rules := &fakeRules{}
	devices := &fakeDevices{}
	service := NewService(store, Owners{
		Connectors:   connectors,
		Rules:        rules,
		Devices:      devices,
		Forwarders:   &fakeForwarders{},
		Models:       &fakeModels{},
		Managers:     &fakeManagers{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deployed.Status != StatusRunning || deployed.ActiveGeneration != deployed.Generation {
		t.Fatalf("healthy read-back did not promote the deployment: %+v", deployed)
	}
	if deployed.PendingGeneration != 0 {
		t.Fatalf("pending generation must clear after promotion: %+v", deployed)
	}
	if connectors.creates != 2 || len(connectors.specs) != 2 {
		t.Fatalf("expected one inference connector and one northbound filter, got creates=%d specs=%d", connectors.creates, len(connectors.specs))
	}
	if countConnectorType(connectors.specs, "edge-inference") != 1 ||
		countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatalf("unexpected generated connector types: %+v", connectors.specs)
	}
	if rules.groups != 1 || rules.drafts != 1 || !rules.activated {
		t.Fatalf("expected one generated rule group/version: %+v", rules)
	}
	if devices.devices["face-access-control-conclusions-1"].RuleChainGroupID == "" {
		t.Fatal("conclusion device was not bound to the generated rule group")
	}
	snapshot, err := store.GetExpansion(context.Background(), "tenant", instance.ID, deployed.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != ExpansionActive {
		t.Fatalf("expansion status = %s, want ACTIVE", snapshot.Status)
	}
	if len(snapshot.Resources) == 0 {
		t.Fatal("owner resources were not recorded")
	}

	monitor, err := service.Monitor(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Status != StatusRunning || len(monitor.Cameras) != 1 {
		t.Fatalf("unexpected monitor: %+v", monitor)
	}
	if monitor.Cameras[0].State != "APPLIED" || monitor.Cameras[0].HealthSource != "connector" {
		t.Fatalf("unexpected camera health: %+v", monitor.Cameras[0])
	}
}

func TestMonitorOptionalJobMetricsPreserveAvailability(t *testing.T) {
	zero := uint64(0)
	for _, test := range []struct {
		name          string
		metrics       JobMetrics
		available     bool
		readErr       error
		wantSource    string
		wantZeroCount bool
	}{
		{name: "explicit zero is available", metrics: JobMetrics{SuccessTotal: &zero}, available: true, wantSource: "connector+job_metrics", wantZeroCount: true},
		{name: "unavailable remains absent", wantSource: "connector"},
		{name: "reader error does not fabricate metrics", metrics: JobMetrics{SuccessTotal: &zero}, available: true, readErr: errors.New("status store unavailable"), wantSource: "connector"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			service := NewService(store, Owners{
				Connectors: &fakeConnectors{state: "APPLIED"}, Rules: &fakeRules{},
				Devices: &fakeDevices{}, Forwarders: &fakeForwarders{}, Models: &fakeModels{},
				Managers: &fakeManagers{}, Companions: &fakeCompanions{},
				Metrics:      &fakeMetrics{metrics: test.metrics, available: test.available, err: test.readErr},
				Distribution: &fakeDistributor{},
			}, nil)
			template, input := validTemplateInput(t, "meter-reading")
			instance := createTestInstance(t, service, template, input)
			deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			monitor, err := service.Monitor(context.Background(), "tenant", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			if deployed.Status != StatusRunning || monitor.Status != StatusRunning || len(monitor.Cameras) != 1 {
				t.Fatalf("metrics reader changed connector lifecycle health: deployed=%+v monitor=%+v", deployed, monitor)
			}
			camera := monitor.Cameras[0]
			if camera.HealthSource != test.wantSource {
				t.Fatalf("health_source=%q want %q", camera.HealthSource, test.wantSource)
			}
			if test.wantZeroCount {
				if camera.SuccessTotal == nil || *camera.SuccessTotal != 0 {
					t.Fatalf("reported zero was lost: %+v", camera)
				}
			} else if camera.SuccessTotal != nil {
				t.Fatalf("unavailable/error metrics fabricated a counter: %+v", camera)
			}
			if camera.ErrorTotal != nil || camera.P50MS != nil || camera.P95MS != nil || camera.EventsPerSecond != nil {
				t.Fatalf("independent absent metrics were fabricated: %+v", camera)
			}
		})
	}
}

func TestPendingConnectorKeepsDeploymentBehindActiveGeneration(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "PENDING"}
	service := NewService(store, Owners{
		Connectors:   connectors,
		Rules:        &fakeRules{},
		Devices:      &fakeDevices{},
		Forwarders:   &fakeForwarders{},
		Models:       &fakeModels{},
		Managers:     &fakeManagers{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "meter-reading")
	instance := createTestInstance(t, service, template, input)

	deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deployed.Status != StatusDeploying || deployed.ActiveGeneration != 0 ||
		deployed.PendingGeneration != deployed.Generation {
		t.Fatalf("pending connector must not advance active generation: %+v", deployed)
	}

	connectors.state = "APPLIED"
	monitor, err := service.Monitor(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Status != StatusRunning || monitor.ActiveGeneration != deployed.Generation {
		t.Fatalf("healthy monitor did not promote pending generation: %+v", monitor)
	}
}

func TestGenerationFenceRejectsStaleEditor(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)
	_, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
		Name:               "another editor",
		ExpectedGeneration: instance.Generation + 1,
	})
	if !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("expected generation conflict, got %v", err)
	}
	updated, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
		Name:               instance.Name,
		Parameters:         instance.Parameters,
		Assembly:           instance.Assembly,
		ExpectedGeneration: instance.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != 2 {
		t.Fatalf("generation = %d, want 2", updated.Generation)
	}
}

func TestUndeployRemovesOwnedResourcesAndRestoresBinding(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	rules := &fakeRules{}
	devices := &fakeDevices{}
	service := NewService(store, Owners{
		Connectors:   connectors,
		Rules:        rules,
		Devices:      devices,
		Forwarders:   &fakeForwarders{},
		Models:       &fakeModels{},
		Managers:     &fakeManagers{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	undeployed, err := service.Undeploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if undeployed.Status != StatusUndeployed || undeployed.ActiveGeneration != 0 {
		t.Fatalf("unexpected undeploy result: %+v", undeployed)
	}
	if countConnectorType(connectors.specs, "edge-inference") != 0 {
		t.Fatal("generated inference connector was not removed")
	}
	if countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatal("last contributor must quiesce rather than delete the singleton filter")
	}
	for _, spec := range connectors.specs {
		if spec.ConnectorType == "edge-northbound-filter" {
			var config map[string]any
			if err := json.Unmarshal(spec.Config, &config); err != nil {
				t.Fatal(err)
			}
			if config["enabled"] != false || config["default_action"] != "allow" {
				t.Fatalf("quiescent filter config = %+v", config)
			}
		}
	}
	if rules.groups != 0 {
		t.Fatal("generated rule group was not removed")
	}
	if got := devices.devices["face-access-control-conclusions-1"].RuleChainGroupID; got != "default-group" {
		t.Fatalf("previous device binding = %q, want default-group", got)
	}
}

func TestCreateTemplateRejectsDuplicatesAndExecutableContent(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	first, err := service.CreateTemplate(context.Background(), "tenant", "tester", Template{
		ID: "custom", Version: "1.0.0", Name: "Custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTemplate(context.Background(), "tenant", "tester", Template{
		ID: "custom", Version: "1.0.0", Name: "Custom duplicate",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate id@version conflict, got %v", err)
	}
	custom := Template{
		ID: "custom-script", Version: "1.0.0", Name: "Custom script",
		Assembly: Assembly{Nodes: []Node{{
			ID: "rule", Kind: NodeKindRule, Name: "Rule",
			Asset: map[string]any{"script": "return true"},
		}}},
	}
	if _, err := service.CreateTemplate(context.Background(), "tenant", "tester", custom); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected executable content rejection, got %v", err)
	}
	if first.SchemaVersion != TemplateSchemaVersion {
		t.Fatalf("custom template metadata not normalized: %+v", first)
	}
}

func TestDeleteTemplateLifecycle(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	if err := service.DeleteTemplate(context.Background(), "tenant", "missing", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing template not found, got %v", err)
	}
	custom, err := service.CreateTemplate(context.Background(), "tenant", "tester", Template{
		ID: "custom-delete", Version: "1.0.0", Name: "Custom delete",
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := createTestInstance(t, service, custom, ExpansionInput{
		Parameters: map[string]any{},
		Assembly:   custom.Assembly,
	})
	if err := service.DeleteTemplate(context.Background(), "tenant", custom.ID, custom.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected referenced template delete conflict, got %v", err)
	}
	if err := service.DeleteInstance(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTemplate(context.Background(), "tenant", custom.ID, custom.Version); err != nil {
		t.Fatalf("delete unused custom template: %v", err)
	}
	if err := service.DeleteTemplate(context.Background(), "tenant", custom.ID, custom.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted template not found, got %v", err)
	}
}

func TestManagerDeliveryStableIdentityOrderingAndNotFoundRecreate(t *testing.T) {
	newService := func() (*Service, *memoryStore, *fakeConnectors, *fakeManagers, *[]string) {
		store := newMemoryStore()
		calls := []string{}
		connectors := &fakeConnectors{state: "APPLIED", calls: &calls}
		managers := &fakeManagers{fakeConnectors: fakeConnectors{state: "APPLIED", calls: &calls}}
		service := NewService(store, Owners{
			Connectors: connectors, Managers: managers, Rules: &fakeRules{},
			Devices: &fakeDevices{}, Forwarders: &fakeForwarders{}, Models: &fakeModels{},
			Companions:   &fakeCompanions{},
			Distribution: &fakeDistributor{},
		}, nil)
		return service, store, connectors, managers, &calls
	}
	advance := func(t *testing.T, service *Service, instance Instance) Instance {
		t.Helper()
		updated, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
			Name: instance.Name, Parameters: instance.Parameters, Assembly: instance.Assembly,
			ExpectedGeneration: instance.Generation,
		})
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}
	managerResource := func(t *testing.T, store *memoryStore, instanceID string, generation int64) Resource {
		t.Helper()
		snapshot, err := store.GetExpansion(context.Background(), "tenant", instanceID, generation)
		if err != nil {
			t.Fatal(err)
		}
		for _, resource := range snapshot.Resources {
			if resource.Kind == ArtifactKindPipelineRegistration {
				return resource
			}
		}
		t.Fatal("manager resource was not recorded")
		return Resource{}
	}

	t.Run("stable update and dependency order", func(t *testing.T) {
		service, store, _, managers, calls := newService()
		template, input := validTemplateInput(t, "meter-reading")
		instance := createTestInstance(t, service, template, input)
		deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
		if err != nil {
			t.Fatal(err)
		}
		first := managerResource(t, store, instance.ID, deployed.Generation)
		managerIndex, bindingIndex := -1, -1
		for index, call := range *calls {
			if call == "create:edge-manager-resource" && managerIndex < 0 {
				managerIndex = index
			}
			if call == "create:edge-inference" && bindingIndex < 0 {
				bindingIndex = index
			}
		}
		if managerIndex < 0 || bindingIndex < 0 || managerIndex > bindingIndex {
			t.Fatalf("manager registration must precede inference binding: %v", *calls)
		}

		updated := advance(t, service, deployed)
		if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
			t.Fatal(err)
		}
		second := managerResource(t, store, instance.ID, updated.Generation)
		if second.ResourceID != first.ResourceID || managers.creates != 1 || managers.updates != 1 {
			t.Fatalf("manager spec identity was not stable: first=%+v second=%+v creates=%d updates=%d",
				first, second, managers.creates, managers.updates)
		}
	})

	t.Run("not found safely recreates", func(t *testing.T) {
		service, store, _, managers, _ := newService()
		template, input := validTemplateInput(t, "meter-reading")
		instance := createTestInstance(t, service, template, input)
		deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
		if err != nil {
			t.Fatal(err)
		}
		first := managerResource(t, store, instance.ID, deployed.Generation)
		updated := advance(t, service, deployed)
		managers.updateErr = connector.ErrNotFound
		if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
			t.Fatal(err)
		}
		second := managerResource(t, store, instance.ID, updated.Generation)
		if second.ResourceID == first.ResourceID || managers.creates != 2 {
			t.Fatalf("ErrNotFound did not safely recreate: first=%+v second=%+v creates=%d",
				first, second, managers.creates)
		}
	})
}

func TestManagedExternalServableFailsBeforeAnySideEffect(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	managers := &fakeManagers{fakeConnectors: fakeConnectors{state: "APPLIED"}}
	rules := &fakeRules{}
	service := NewService(store, Owners{
		Connectors: connectors, Managers: managers, Rules: rules,
		Devices: &fakeDevices{}, Forwarders: &fakeForwarders{}, Models: &fakeModels{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	setAsset(t, &input.Assembly, "camera", "target_device", "")
	setAsset(t, &input.Assembly, "external", "definition", map[string]any{"servableId": "external-face-1"})
	instance := createTestInstance(t, service, template, input)
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("managed ExternalServablesJSON must fail closed, got %v", err)
	}
	stored, err := service.GetInstance(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(stored.Validation, "managed_external_resource_unavailable") {
		t.Fatalf("missing managed external preflight issue: %+v", stored.Validation)
	}
	if connectors.creates != 0 || managers.creates != 0 || rules.groups != 0 {
		t.Fatalf("blocked preflight caused owner side effects: connectors=%d managers=%d rules=%d",
			connectors.creates, managers.creates, rules.groups)
	}
}

func TestManagerHealthRequiresCurrentObservedReadback(t *testing.T) {
	for _, test := range []struct {
		name            string
		state           string
		reason          string
		missingObserved bool
		wantStatus      string
		wantIssue       string
	}{
		{name: "reference entry missing", state: "DEGRADED", reason: "manager-entry-missing", wantStatus: StatusDegraded, wantIssue: "manager-entry-missing"},
		{name: "applied without readback stays pending", state: "APPLIED", missingObserved: true, wantStatus: StatusDeploying, wantIssue: "manager_readback_pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			managers := &fakeManagers{fakeConnectors: fakeConnectors{state: test.state},
				reason: test.reason, missingObserved: test.missingObserved}
			service := NewService(store, Owners{
				Connectors: &fakeConnectors{state: "APPLIED"}, Managers: managers,
				Rules: &fakeRules{}, Devices: &fakeDevices{}, Forwarders: &fakeForwarders{},
				Models: &fakeModels{}, Companions: &fakeCompanions{},
				Distribution: &fakeDistributor{},
			}, nil)
			template, input := validTemplateInput(t, "meter-reading")
			instance := createTestInstance(t, service, template, input)
			deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			if deployed.Status != test.wantStatus {
				t.Fatalf("manager health status=%s want %s: %+v", deployed.Status, test.wantStatus, deployed)
			}
			monitor, err := service.Monitor(context.Background(), "tenant", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range monitor.Issues {
				if issue.Code == test.wantIssue {
					found = true
				}
			}
			if !found {
				t.Fatalf("manager health issue %s missing: %+v", test.wantIssue, monitor.Issues)
			}
		})
	}
}

func TestOrdinaryDeployPrunesRemovedManagerAndRetryPersistsFailure(t *testing.T) {
	store := newMemoryStore()
	managers := &fakeManagers{fakeConnectors: fakeConnectors{state: "APPLIED"}}
	service := NewService(store, Owners{
		Connectors: &fakeConnectors{state: "APPLIED"}, Managers: managers,
		Rules: &fakeRules{}, Devices: &fakeDevices{}, Forwarders: &fakeForwarders{},
		Models: &fakeModels{}, Companions: &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	setAsset(t, &input.Assembly, "camera", "target_device", "")
	instance := createTestInstance(t, service, template, input)
	deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	external := resourceForKind(t, store, instance.ID, deployed.Generation, ArtifactKindExternalServable)
	pipeline := resourceForKind(t, store, instance.ID, deployed.Generation, ArtifactKindPipelineRegistration)

	nextAssembly := cloneAssembly(t, instance.Assembly)
	nextAssembly.Nodes = slicesDeleteFunc(nextAssembly.Nodes, func(node Node) bool { return node.ID == "external" })
	nextAssembly.Edges = slicesDeleteFunc(nextAssembly.Edges, func(edge Edge) bool { return edge.ID == "external-pipeline" })
	updated, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
		Name: instance.Name, Parameters: instance.Parameters, Assembly: nextAssembly,
		ExpectedGeneration: instance.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	managers.deleteErr = errors.New("delete outbox unavailable")
	failed, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err == nil {
		t.Fatal("pruning failure must fail the deployment")
	}
	if failed.Status != StatusFailed || failed.LastError == "" || failed.ActiveGeneration != deployed.Generation ||
		failed.PendingGeneration != 0 || updated.Generation != 2 {
		t.Fatalf("pruning failure was not persisted with active rollback state: %+v", failed)
	}

	managers.deleteErr = nil
	retried, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatalf("retry after pruning failure: %v", err)
	}
	if retried.Status != StatusRunning || retried.ActiveGeneration != updated.Generation {
		t.Fatalf("pruning retry did not activate target: %+v", retried)
	}
	if !containsString(managers.deletes, external.ResourceID) {
		t.Fatalf("ordinary generation did not prune removed external manager: %v", managers.deletes)
	}
	if containsString(managers.deletes, pipeline.ResourceID) {
		t.Fatalf("ordinary generation pruned a retained shared pipeline: %v", managers.deletes)
	}
}

func TestRollbackRestoresTargetDependenciesBeforePruning(t *testing.T) {
	store := newMemoryStore()
	calls := []string{}
	managers := &fakeManagers{fakeConnectors: fakeConnectors{state: "APPLIED", calls: &calls}}
	service := NewService(store, Owners{
		Connectors: &fakeConnectors{state: "APPLIED"}, Managers: managers,
		Rules: &fakeRules{}, Devices: &fakeDevices{}, Forwarders: &fakeForwarders{},
		Models: &fakeModels{}, Companions: &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	addExternalServable(&input.Assembly)
	setAsset(t, &input.Assembly, "camera", "target_device", "")
	instance := createTestInstance(t, service, template, input)
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	oldPipeline := resourceForKind(t, store, instance.ID, 1, ArtifactKindPipelineRegistration)
	oldExternal := resourceForKind(t, store, instance.ID, 1, ArtifactKindExternalServable)

	nextAssembly := cloneAssembly(t, instance.Assembly)
	nextAssembly.Nodes = slicesDeleteFunc(nextAssembly.Nodes, func(node Node) bool { return node.ID == "external" })
	nextAssembly.Edges = slicesDeleteFunc(nextAssembly.Edges, func(edge Edge) bool { return edge.ID == "external-pipeline" })
	setAsset(t, &nextAssembly, "pipeline", "pipeline_id", "face-recognition-v2")
	if _, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
		Name: instance.Name, Parameters: instance.Parameters, Assembly: nextAssembly, ExpectedGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	newPipeline := resourceForKind(t, store, instance.ID, 2, ArtifactKindPipelineRegistration)
	if newPipeline.ResourceID == oldPipeline.ResourceID || !containsString(managers.deletes, oldPipeline.ResourceID) {
		t.Fatalf("generation 2 did not replace/prune changed manager identity: old=%+v new=%+v deletes=%v",
			oldPipeline, newPipeline, managers.deletes)
	}
	marker := len(calls)
	rolledBack, err := service.Rollback(context.Background(), "tenant", instance.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ActiveGeneration != 1 || rolledBack.Status != StatusRunning {
		t.Fatalf("rollback did not activate restored generation: %+v", rolledBack)
	}
	restoredPipeline := resourceForKind(t, store, instance.ID, 1, ArtifactKindPipelineRegistration)
	restoredExternal := resourceForKind(t, store, instance.ID, 1, ArtifactKindExternalServable)
	if restoredPipeline.ResourceID == oldPipeline.ResourceID || restoredExternal.ResourceID == oldExternal.ResourceID {
		t.Fatalf("missing target dependencies were not recreated: pipeline=%+v external=%+v", restoredPipeline, restoredExternal)
	}
	deleteIndex, createCount := -1, 0
	for index := marker; index < len(calls); index++ {
		if calls[index] == "create:edge-manager-resource" {
			createCount++
		}
		if calls[index] == "delete:"+newPipeline.ResourceID {
			deleteIndex = index
		}
	}
	if createCount < 2 || deleteIndex < 0 {
		t.Fatalf("rollback did not recreate target and prune current-only manager: %v", calls[marker:])
	}
	for index := marker; index < deleteIndex; index++ {
		if calls[index] == "create:edge-manager-resource" {
			createCount--
		}
	}
	if createCount != 0 {
		t.Fatalf("current-only manager was pruned before target dependencies restored: %v", calls[marker:])
	}
}

func TestNorthboundFilterMergesLiveContributionsAndQuiescesLastRemoval(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	service := NewService(store, Owners{
		Connectors: connectors, Managers: &fakeManagers{}, Rules: &fakeRules{},
		Devices: &fakeDevices{}, Forwarders: &fakeForwarders{}, Models: &fakeModels{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
	faceTemplate, faceInput := validTemplateInput(t, "face-access-control")
	meterTemplate, meterInput := validTemplateInput(t, "meter-reading")
	face := createTestInstance(t, service, faceTemplate, faceInput)
	meter := createTestInstance(t, service, meterTemplate, meterInput)
	if _, err := service.Deploy(context.Background(), "tenant", face.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(context.Background(), "tenant", meter.ID); err != nil {
		t.Fatal(err)
	}
	faceFilter := resourceForKind(t, store, face.ID, face.Generation, ArtifactKindNorthboundFilter)
	meterFilter := resourceForKind(t, store, meter.ID, meter.Generation, ArtifactKindNorthboundFilter)
	if faceFilter.ResourceID == "" || faceFilter.ResourceID != meterFilter.ResourceID {
		t.Fatalf("gateway filter was not shared: face=%+v meter=%+v", faceFilter, meterFilter)
	}
	if countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatalf("expected one singleton filter, got %+v", connectors.specs)
	}
	merged := connectors.specs[faceFilter.ResourceID]
	var mergedConfig map[string]any
	if err := json.Unmarshal(merged.Config, &mergedConfig); err != nil {
		t.Fatal(err)
	}
	rules, ok := mergedConfig["rules"].([]any)
	if !ok || len(rules) != 4 {
		t.Fatalf("merged filter should contain two allow/deny pairs: %#v", mergedConfig["rules"])
	}

	if _, err := service.Undeploy(context.Background(), "tenant", meter.ID); err != nil {
		t.Fatal(err)
	}
	if countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatal("removing one contributor must not delete the shared filter")
	}
	var faceConfig map[string]any
	if err := json.Unmarshal(connectors.specs[faceFilter.ResourceID].Config, &faceConfig); err != nil {
		t.Fatal(err)
	}
	rawRules, _ := json.Marshal(faceConfig["rules"])
	if strings.Contains(string(rawRules), "meter-reading-edgex-conclusions") {
		t.Fatalf("removed meter contributor survived in filter: %s", rawRules)
	}
	if !strings.Contains(string(rawRules), "face-access-control-edgex-conclusions") {
		t.Fatalf("remaining face contributor lost from filter: %s", rawRules)
	}

	if _, err := service.Undeploy(context.Background(), "tenant", face.ID); err != nil {
		t.Fatal(err)
	}
	var quiescent map[string]any
	if err := json.Unmarshal(connectors.specs[faceFilter.ResourceID].Config, &quiescent); err != nil {
		t.Fatal(err)
	}
	if quiescent["enabled"] != false || quiescent["default_action"] != "allow" {
		t.Fatalf("last removal did not quiesce singleton: %+v", quiescent)
	}
}

func TestNorthboundFilterRollbackRestoresPriorSelectors(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	service := newNorthboundTestService(store, connectors)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	firstFilter := resourceForKind(t, store, instance.ID, 1, ArtifactKindNorthboundFilter)

	nextAssembly := cloneAssembly(t, instance.Assembly)
	setAsset(t, &nextAssembly, "northbound", "edgex_device_name", "gen2-edgex-conclusions")
	updated, err := service.UpdateInstance(context.Background(), "tenant", instance.ID, InstanceUpdate{
		Name: instance.Name, Parameters: instance.Parameters, Assembly: nextAssembly,
		ExpectedGeneration: instance.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	gen2Config := mustNorthboundFilterConfig(t, connectors, firstFilter.ResourceID)
	if !northboundRulesContain(gen2Config, "gen2-edgex-conclusions") ||
		northboundRulesContain(gen2Config, "face-access-control-edgex-conclusions") {
		t.Fatalf("generation 2 filter did not replace raw selectors: %+v", gen2Config)
	}

	if _, err := service.Rollback(context.Background(), "tenant", instance.ID, 1); err != nil {
		t.Fatal(err)
	}
	rolledBackFilter := resourceForKind(t, store, instance.ID, 1, ArtifactKindNorthboundFilter)
	if rolledBackFilter.ResourceID != firstFilter.ResourceID {
		t.Fatalf("rollback created another singleton connector: first=%+v rolled=%+v", firstFilter, rolledBackFilter)
	}
	restoredConfig := mustNorthboundFilterConfig(t, connectors, rolledBackFilter.ResourceID)
	if !northboundRulesContain(restoredConfig, "face-access-control-edgex-conclusions") ||
		northboundRulesContain(restoredConfig, "gen2-edgex-conclusions") {
		t.Fatalf("rollback did not restore generation 1 selectors: %+v", restoredConfig)
	}
	if updated.Generation != 2 {
		t.Fatalf("unexpected updated generation: %+v", updated)
	}
}

func TestNorthboundFilterIncludesPendingCrossInstanceContribution(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	service := newNorthboundTestService(store, connectors)
	faceTemplate, faceInput := validTemplateInput(t, "face-access-control")
	meterTemplate, meterInput := validTemplateInput(t, "meter-reading")
	face := createTestInstance(t, service, faceTemplate, faceInput)
	meter := createTestInstance(t, service, meterTemplate, meterInput)

	meterInstance := store.instances["tenant"][meter.ID]
	meterInstance.Status = StatusDeploying
	meterInstance.PendingGeneration = 1
	store.instances["tenant"][meter.ID] = meterInstance
	meterSnapshot := store.expansion["tenant"][meter.ID][1]
	meterSnapshot.Status = ExpansionDeploying
	store.expansion["tenant"][meter.ID][1] = meterSnapshot

	if _, err := service.Deploy(context.Background(), "tenant", face.ID); err != nil {
		t.Fatal(err)
	}
	faceFilter := resourceForKind(t, store, face.ID, face.Generation, ArtifactKindNorthboundFilter)
	merged := mustNorthboundFilterConfig(t, connectors, faceFilter.ResourceID)
	if !northboundRulesContain(merged, "face-access-control-edgex-conclusions") ||
		!northboundRulesContain(merged, "meter-reading-edgex-conclusions") {
		t.Fatalf("pending cross-instance contribution was not merged: %+v", merged)
	}

	meterInstance = store.instances["tenant"][meter.ID]
	meterInstance.Status = StatusFailed
	meterInstance.PendingGeneration = 0
	store.instances["tenant"][meter.ID] = meterInstance
	meterSnapshot = store.expansion["tenant"][meter.ID][1]
	meterSnapshot.Status = ExpansionFailed
	store.expansion["tenant"][meter.ID][1] = meterSnapshot

	if _, err := service.Deploy(context.Background(), "tenant", face.ID); err != nil {
		t.Fatal(err)
	}
	if countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatalf("same-generation retry duplicated the singleton filter: %+v", connectors.specs)
	}
	faceOnly := mustNorthboundFilterConfig(t, connectors, faceFilter.ResourceID)
	if !northboundRulesContain(faceOnly, "face-access-control-edgex-conclusions") ||
		northboundRulesContain(faceOnly, "meter-reading-edgex-conclusions") {
		t.Fatalf("failed pending contribution survived reconciliation: %+v", faceOnly)
	}
}

func TestNorthboundFilterRecreatesMissingConnector(t *testing.T) {
	store := newMemoryStore()
	connectors := &fakeConnectors{state: "APPLIED"}
	service := newNorthboundTestService(store, connectors)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	first := resourceForKind(t, store, instance.ID, 1, ArtifactKindNorthboundFilter)
	delete(connectors.specs, first.ResourceID)

	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err != nil {
		t.Fatal(err)
	}
	recreated := resourceForKind(t, store, instance.ID, 1, ArtifactKindNorthboundFilter)
	if recreated.ResourceID == "" || recreated.ResourceID == first.ResourceID {
		t.Fatalf("missing connector was not recreated: first=%+v recreated=%+v", first, recreated)
	}
	if countConnectorType(connectors.specs, "edge-northbound-filter") != 1 {
		t.Fatalf("recreation left duplicate filter connectors: %+v", connectors.specs)
	}
	config := mustNorthboundFilterConfig(t, connectors, recreated.ResourceID)
	if !northboundRulesContain(config, "face-access-control-edgex-conclusions") {
		t.Fatalf("recreated filter lost its selectors: %+v", config)
	}
}

func newNorthboundTestService(store Store, connectors ConnectorOwner) *Service {
	return NewService(store, Owners{
		Connectors: connectors, Managers: &fakeManagers{}, Rules: &fakeRules{},
		Devices: &fakeDevices{}, Forwarders: &fakeForwarders{}, Models: &fakeModels{},
		Companions:   &fakeCompanions{},
		Distribution: &fakeDistributor{},
	}, nil)
}

func mustNorthboundFilterConfig(t *testing.T, connectors *fakeConnectors, specID string) map[string]any {
	t.Helper()
	spec, ok := connectors.specs[specID]
	if !ok {
		t.Fatalf("northbound filter connector %s not found: %+v", specID, connectors.specs)
	}
	var config map[string]any
	if err := json.Unmarshal(spec.Config, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func northboundRulesContain(config map[string]any, want string) bool {
	raw, err := json.Marshal(config["rules"])
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), want)
}

func TestSharedManagerLastLiveReferenceIncludingPending(t *testing.T) {
	newFixture := func(t *testing.T, blocker *blockingConnectors) (*Service, *memoryStore, *fakeManagers, Template, ExpansionInput) {
		t.Helper()
		store := newMemoryStore()
		managers := &fakeManagers{fakeConnectors: fakeConnectors{state: "APPLIED"}}
		service := NewService(store, Owners{
			Connectors: blocker, Managers: managers, Rules: &fakeRules{}, Devices: &fakeDevices{},
			Forwarders: &fakeForwarders{}, Models: &fakeModels{}, Companions: &fakeCompanions{},
			Distribution: &fakeDistributor{},
		}, nil)
		template, input := managerOnlyTemplate(t)
		if _, err := service.CreateTemplate(context.Background(), "tenant", "tester", template); err != nil {
			t.Fatal(err)
		}
		return service, store, managers, template, input
	}
	create := func(t *testing.T, service *Service, template Template, input ExpansionInput, name string, assembly Assembly) Instance {
		t.Helper()
		instance, err := service.CreateInstance(context.Background(), "tenant", InstanceInput{
			Name: name, TemplateID: template.ID, TemplateVersion: template.Version,
			Parameters: input.Parameters, Assembly: assembly,
		})
		if err != nil {
			t.Fatal(err)
		}
		return instance
	}

	t.Run("cross instance last reference", func(t *testing.T) {
		blocker := newBlockingConnectors()
		service, store, managers, template, input := newFixture(t, blocker)
		first := create(t, service, template, input, "first", input.Assembly)
		second := create(t, service, template, input, "second", input.Assembly)
		if _, err := service.Deploy(context.Background(), "tenant", first.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Deploy(context.Background(), "tenant", second.ID); err != nil {
			t.Fatal(err)
		}
		firstResource := resourceForKind(t, store, first.ID, 1, ArtifactKindPipelineRegistration)
		secondResource := resourceForKind(t, store, second.ID, 1, ArtifactKindPipelineRegistration)
		if firstResource.ResourceID != secondResource.ResourceID || managers.creates != 1 {
			t.Fatalf("identical live manager entries did not share one spec: first=%+v second=%+v creates=%d",
				firstResource, secondResource, managers.creates)
		}
		if _, err := service.Undeploy(context.Background(), "tenant", first.ID); err != nil {
			t.Fatal(err)
		}
		if len(managers.deletes) != 0 {
			t.Fatalf("shared manager deleted while second instance was active: %v", managers.deletes)
		}
		if _, err := service.Undeploy(context.Background(), "tenant", second.ID); err != nil {
			t.Fatal(err)
		}
		if !containsString(managers.deletes, firstResource.ResourceID) {
			t.Fatalf("last live reference did not release manager: %v", managers.deletes)
		}
	})

	t.Run("pending generation is live", func(t *testing.T) {
		blocker := newBlockingConnectors()
		service, store, managers, template, input := newFixture(t, blocker)
		first := create(t, service, template, input, "active-owner", input.Assembly)
		if _, err := service.Deploy(context.Background(), "tenant", first.ID); err != nil {
			t.Fatal(err)
		}
		manager := resourceForKind(t, store, first.ID, 1, ArtifactKindPipelineRegistration)

		// Generation 1 of the second instance has no manager artifact. Its
		// generation 2 is the sole other live reference while apply is blocked.
		second := create(t, service, template, input, "pending-reference", Assembly{Nodes: []Node{}, Edges: []Edge{}})
		if _, err := service.Deploy(context.Background(), "tenant", second.ID); err != nil {
			t.Fatal(err)
		}
		updated, err := service.UpdateInstance(context.Background(), "tenant", second.ID, InstanceUpdate{
			Name: second.Name, Parameters: second.Parameters, Assembly: input.Assembly,
			ExpectedGeneration: second.Generation,
		})
		if err != nil {
			t.Fatal(err)
		}
		blocker.arm()
		deployResult := make(chan error, 1)
		go func() {
			_, err := service.Deploy(context.Background(), "tenant", second.ID)
			deployResult <- err
		}()
		select {
		case <-blocker.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for pending deployment")
		}
		pendingResource := resourceForKind(t, store, second.ID, updated.Generation, ArtifactKindPipelineRegistration)
		if pendingResource.ResourceID != manager.ResourceID {
			t.Fatalf("pending generation did not record the shared manager: %+v", pendingResource)
		}
		if _, err := service.Undeploy(context.Background(), "tenant", first.ID); err != nil {
			t.Fatal(err)
		}
		if len(managers.deletes) != 0 {
			t.Fatalf("manager deleted while only pending generation still referenced it: %v", managers.deletes)
		}
		close(blocker.release)
		if err := <-deployResult; err != nil {
			t.Fatal(err)
		}
		if _, err := service.Undeploy(context.Background(), "tenant", second.ID); err != nil {
			t.Fatal(err)
		}
		if !containsString(managers.deletes, manager.ResourceID) {
			t.Fatalf("last pending-then-active reference did not release manager: %v", managers.deletes)
		}
	})
}

func managerOnlyTemplate(t *testing.T) (Template, ExpansionInput) {
	t.Helper()
	template := loadSampleTemplate(t, "meter-reading", "1.0.0")
	_, input := validTemplateInput(t, "meter-reading")
	keep := func(assembly Assembly) Assembly {
		out := Assembly{Nodes: []Node{}, Edges: []Edge{}}
		ids := map[string]bool{}
		for _, node := range assembly.Nodes {
			if node.Kind == NodeKindCamera || node.Kind == NodeKindPipeline || node.Kind == NodeKindModel {
				out.Nodes = append(out.Nodes, node)
				ids[node.ID] = true
			}
		}
		for _, edge := range assembly.Edges {
			if ids[edge.Source] && ids[edge.Target] {
				out.Edges = append(out.Edges, edge)
			}
		}
		return out
	}
	template.ID = "manager-only"
	template.Requires = map[string]string{}
	template.Assembly = keep(template.Assembly)
	input.Assembly = keep(input.Assembly)
	return template, input
}

func cloneAssembly(t *testing.T, assembly Assembly) Assembly {
	t.Helper()
	raw, err := json.Marshal(assembly)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Assembly
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func slicesDeleteFunc[T any](values []T, remove func(T) bool) []T {
	out := values[:0]
	for _, value := range values {
		if !remove(value) {
			out = append(out, value)
		}
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func resourceForKind(t *testing.T, store *memoryStore, instanceID string, generation int64, kind string) Resource {
	t.Helper()
	snapshot, err := store.GetExpansion(context.Background(), "tenant", instanceID, generation)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("resource kind %s not found in generation %d", kind, generation)
	return Resource{}
}

func countConnectorType(specs map[string]connector.Spec, connectorType string) int {
	count := 0
	for _, spec := range specs {
		if spec.ConnectorType == connectorType {
			count++
		}
	}
	return count
}

func createTestInstance(t *testing.T, service *Service, template Template, input ExpansionInput) Instance {
	t.Helper()
	// Packs must be imported before instantiation; the seed is idempotent for
	// tests that already published the same version.
	if _, err := service.CreateTemplate(context.Background(), "tenant", "test", template); err != nil &&
		!errors.Is(err, ErrConflict) {
		t.Fatalf("seed template %s/%s: %v", template.ID, template.Version, err)
	}
	instance, err := service.CreateInstance(context.Background(), "tenant", InstanceInput{
		Name:            template.ID + "-test",
		TemplateID:      template.ID,
		TemplateVersion: template.Version,
		Parameters:      input.Parameters,
		Assembly:        input.Assembly,
	})
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

// --- in-memory store -------------------------------------------------------

type memoryStore struct {
	templates map[string]map[string]Template
	instances map[string]map[string]Instance
	expansion map[string]map[string]map[int64]ExpansionSnapshot
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		templates: map[string]map[string]Template{},
		instances: map[string]map[string]Instance{},
		expansion: map[string]map[string]map[int64]ExpansionSnapshot{},
	}
}

func templateKey(id, version string) string { return id + "@" + version }

func (m *memoryStore) ListTemplates(_ context.Context, tenantID string) ([]Template, error) {
	out := []Template{}
	for _, template := range m.templates[tenantID] {
		out = append(out, template)
	}
	sort.Slice(out, func(i, j int) bool {
		return templateKey(out[i].ID, out[i].Version) < templateKey(out[j].ID, out[j].Version)
	})
	return out, nil
}

func (m *memoryStore) CreateTemplate(_ context.Context, tenantID, _ string, template Template) (Template, error) {
	if m.templates[tenantID] == nil {
		m.templates[tenantID] = map[string]Template{}
	}
	key := templateKey(template.ID, template.Version)
	if _, exists := m.templates[tenantID][key]; exists {
		return Template{}, ErrConflict
	}
	now := time.Now().UTC()
	template.CreatedAt = &now
	m.templates[tenantID][key] = template
	return template, nil
}

func (m *memoryStore) DeleteTemplate(_ context.Context, tenantID, templateID, version string) error {
	key := templateKey(templateID, version)
	if _, exists := m.templates[tenantID][key]; !exists {
		return ErrNotFound
	}
	for _, instance := range m.instances[tenantID] {
		if instance.TemplateID == templateID && instance.TemplateVersion == version {
			return ErrConflict
		}
	}
	delete(m.templates[tenantID], key)
	return nil
}

func (m *memoryStore) GetTemplate(_ context.Context, tenantID, templateID, version string) (Template, error) {
	template, ok := m.templates[tenantID][templateKey(templateID, version)]
	if !ok {
		return Template{}, ErrNotFound
	}
	return template, nil
}

func (m *memoryStore) ListInstances(_ context.Context, tenantID string) ([]Instance, error) {
	out := []Instance{}
	for _, instance := range m.instances[tenantID] {
		out = append(out, cloneInstance(instance))
	}
	return out, nil
}

func (m *memoryStore) GetInstance(_ context.Context, tenantID, instanceID string) (Instance, error) {
	instance, ok := m.instances[tenantID][instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	return cloneInstance(instance), nil
}

func (m *memoryStore) CreateInstance(_ context.Context, tenantID string, instance Instance, expansion Expansion) (Instance, error) {
	if m.instances[tenantID] == nil {
		m.instances[tenantID] = map[string]Instance{}
		m.expansion[tenantID] = map[string]map[int64]ExpansionSnapshot{}
	}
	for _, existing := range m.instances[tenantID] {
		if existing.Name == instance.Name {
			return Instance{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	instance.Generation = 1
	instance.ActiveGeneration = 0
	instance.PendingGeneration = 0
	instance.Status = StatusDraft
	instance.CreatedAt = now
	instance.UpdatedAt = now
	m.instances[tenantID][instance.ID] = cloneInstance(instance)
	m.expansion[tenantID][instance.ID] = map[int64]ExpansionSnapshot{
		expansion.Generation: {Expansion: expansion, Status: ExpansionDraft, Resources: []Resource{}},
	}
	return cloneInstance(instance), nil
}

func (m *memoryStore) UpdateInstance(_ context.Context, tenantID, instanceID string, update InstanceUpdate, expansion Expansion, report ValidationReport) (Instance, error) {
	instance, ok := m.instances[tenantID][instanceID]
	if !ok {
		return Instance{}, ErrNotFound
	}
	if instance.Generation != update.ExpectedGeneration || instance.Status == StatusDeploying {
		return Instance{}, ErrGenerationConflict
	}
	instance.Name = update.Name
	instance.Parameters = update.Parameters
	instance.Assembly = update.Assembly
	instance.Validation = report
	instance.Generation++
	instance.PendingGeneration = 0
	instance.Status = StatusDraft
	instance.LastError = ""
	instance.UpdatedAt = time.Now().UTC()
	if expansion.Generation != instance.Generation {
		return Instance{}, ErrGenerationConflict
	}
	m.instances[tenantID][instanceID] = cloneInstance(instance)
	m.expansion[tenantID][instanceID][expansion.Generation] = ExpansionSnapshot{Expansion: expansion, Status: ExpansionDraft, Resources: []Resource{}}
	return cloneInstance(instance), nil
}

func (m *memoryStore) DeleteInstance(_ context.Context, tenantID, instanceID string) error {
	if _, ok := m.instances[tenantID][instanceID]; !ok {
		return ErrNotFound
	}
	delete(m.instances[tenantID], instanceID)
	delete(m.expansion[tenantID], instanceID)
	return nil
}

func (m *memoryStore) SaveValidation(_ context.Context, tenantID, instanceID string, generation int64, report ValidationReport) error {
	instance, ok := m.instances[tenantID][instanceID]
	if !ok || instance.Generation != generation {
		return ErrGenerationConflict
	}
	instance.Validation = report
	m.instances[tenantID][instanceID] = instance
	return nil
}

func (m *memoryStore) GetExpansion(_ context.Context, tenantID, instanceID string, generation int64) (ExpansionSnapshot, error) {
	snapshot, ok := m.expansion[tenantID][instanceID][generation]
	if !ok {
		return ExpansionSnapshot{}, ErrNotFound
	}
	raw, _ := json.Marshal(snapshot)
	var cloned ExpansionSnapshot
	_ = json.Unmarshal(raw, &cloned)
	return cloned, nil
}

func (m *memoryStore) FindResource(_ context.Context, tenantID, instanceID, artifactKey string) (Resource, bool, error) {
	generations := make([]int64, 0)
	for generation := range m.expansion[tenantID][instanceID] {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] > generations[j] })
	for _, generation := range generations {
		for _, resource := range m.expansion[tenantID][instanceID][generation].Resources {
			if resource.ArtifactKey == artifactKey {
				return resource, true, nil
			}
		}
	}
	return Resource{}, false, nil
}

func (m *memoryStore) liveResources(tenantID string) []LiveResource {
	instanceIDs := make([]string, 0, len(m.instances[tenantID]))
	for instanceID := range m.instances[tenantID] {
		instanceIDs = append(instanceIDs, instanceID)
	}
	sort.Strings(instanceIDs)
	out := []LiveResource{}
	for _, instanceID := range instanceIDs {
		instance := m.instances[tenantID][instanceID]
		generations := sortedUniqueInt64([]int64{instance.ActiveGeneration, instance.PendingGeneration})
		for _, generation := range generations {
			if generation == 0 {
				continue
			}
			for _, resource := range m.expansion[tenantID][instanceID][generation].Resources {
				out = append(out, LiveResource{Resource: resource, InstanceID: instanceID, Generation: generation})
			}
		}
	}
	return out
}

func sortedUniqueInt64(values []int64) []int64 {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func (m *memoryStore) FindLiveResource(_ context.Context, tenantID, kind, ownerID string) (LiveResource, bool, error) {
	if ownerID == "" {
		return LiveResource{}, false, nil
	}
	for _, resource := range m.liveResources(tenantID) {
		if resource.Kind == kind && resource.OwnerID == ownerID {
			return resource, true, nil
		}
	}
	return LiveResource{}, false, nil
}

func (m *memoryStore) ListLiveResources(_ context.Context, tenantID, kind string) ([]LiveResource, error) {
	out := make([]LiveResource, 0)
	for _, resource := range m.liveResources(tenantID) {
		if resource.Kind == kind {
			out = append(out, resource)
		}
	}
	return out, nil
}

func (m *memoryStore) ListLiveExpansions(_ context.Context, tenantID string) ([]LiveExpansion, error) {
	instanceIDs := make([]string, 0, len(m.instances[tenantID]))
	for instanceID := range m.instances[tenantID] {
		instanceIDs = append(instanceIDs, instanceID)
	}
	sort.Strings(instanceIDs)
	out := make([]LiveExpansion, 0)
	for _, instanceID := range instanceIDs {
		instance := m.instances[tenantID][instanceID]
		for _, generation := range sortedUniqueInt64([]int64{instance.ActiveGeneration, instance.PendingGeneration}) {
			if generation == 0 {
				continue
			}
			snapshot, ok := m.expansion[tenantID][instanceID][generation]
			if !ok {
				continue
			}
			raw, _ := json.Marshal(snapshot)
			var cloned ExpansionSnapshot
			_ = json.Unmarshal(raw, &cloned)
			out = append(out, LiveExpansion{
				InstanceID:        instanceID,
				Generation:        generation,
				ExpansionSnapshot: cloned,
			})
		}
	}
	return out, nil
}

func (m *memoryStore) ResourceReferenced(_ context.Context, tenantID string, resource Resource, excludeInstanceID string, excludeGeneration int64) (bool, error) {
	return resourceReferenced(m.liveResources(tenantID), resource, excludeInstanceID, excludeGeneration), nil
}

func (m *memoryStore) RecordResource(_ context.Context, tenantID, instanceID string, desiredGeneration int64, expansion Expansion, resource Resource) error {
	instance, ok := m.instances[tenantID][instanceID]
	if !ok || instance.Generation != desiredGeneration || instance.PendingGeneration != expansion.Generation ||
		instance.Status != StatusDeploying {
		return ErrGenerationConflict
	}
	snapshot := m.expansion[tenantID][instanceID][expansion.Generation]
	replaced := false
	for index := range snapshot.Resources {
		if snapshot.Resources[index].ArtifactKey == resource.ArtifactKey {
			snapshot.Resources[index] = resource
			replaced = true
		}
	}
	if !replaced {
		snapshot.Resources = append(snapshot.Resources, resource)
	}
	m.expansion[tenantID][instanceID][expansion.Generation] = snapshot
	return nil
}

func (m *memoryStore) UpdateDeployment(_ context.Context, tenantID, instanceID string, desiredGeneration int64, patch DeploymentPatch) (Instance, error) {
	instance, ok := m.instances[tenantID][instanceID]
	if !ok || instance.Generation != desiredGeneration ||
		(patch.RequireNotDeploying && instance.Status == StatusDeploying) {
		return Instance{}, ErrGenerationConflict
	}
	instance.Status = patch.Status
	instance.LastError = patch.LastError
	if patch.ActiveGeneration != nil {
		instance.ActiveGeneration = *patch.ActiveGeneration
	}
	if patch.PendingGeneration != nil {
		instance.PendingGeneration = *patch.PendingGeneration
	}
	if instance.Status == StatusDeploying || instance.Status == StatusRunning || instance.Status == StatusDegraded {
		if instance.DeployedAt == nil {
			now := time.Now().UTC()
			instance.DeployedAt = &now
		}
	}
	instance.UpdatedAt = time.Now().UTC()
	m.instances[tenantID][instanceID] = cloneInstance(instance)
	if patch.ExpansionGeneration > 0 && patch.ExpansionStatus != "" {
		snapshot := m.expansion[tenantID][instanceID][patch.ExpansionGeneration]
		snapshot.Status = patch.ExpansionStatus
		m.expansion[tenantID][instanceID][patch.ExpansionGeneration] = snapshot
	}
	return cloneInstance(instance), nil
}

func cloneInstance(instance Instance) Instance {
	raw, _ := json.Marshal(instance)
	var cloned Instance
	_ = json.Unmarshal(raw, &cloned)
	cloned.TemplateSnapshot = instance.TemplateSnapshot
	return cloned
}

// --- fake destination owners ----------------------------------------------

type blockingConnectors struct {
	fakeConnectors
	started chan struct{}
	release chan struct{}
	armed   bool
}

func newBlockingConnectors() *blockingConnectors {
	return &blockingConnectors{
		fakeConnectors: fakeConnectors{state: "APPLIED"},
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (f *blockingConnectors) arm() {
	f.armed = true
	f.started = make(chan struct{})
	f.release = make(chan struct{})
}

func (f *blockingConnectors) CreateSpec(ctx context.Context, tenantID, gatewayID string, in connector.SaveInput) (connector.Spec, error) {
	if f.armed {
		f.armed = false
		close(f.started)
		<-f.release
	}
	return f.fakeConnectors.CreateSpec(ctx, tenantID, gatewayID, in)
}

type fakeConnectors struct {
	state     string
	creates   int
	updates   int
	updateErr error
	deleteErr error
	calls     *[]string
	deletes   []string
	specs     map[string]connector.Spec
}

func (f *fakeConnectors) ensure() {
	if f.specs == nil {
		f.specs = map[string]connector.Spec{}
	}
}

func (f *fakeConnectors) CreateSpec(_ context.Context, tenantID, gatewayID string, in connector.SaveInput) (connector.Spec, error) {
	f.ensure()
	f.creates++
	if f.calls != nil {
		*f.calls = append(*f.calls, "create:"+in.ConnectorType)
	}
	id := "connector-" + string(rune('0'+f.creates))
	f.specs[id] = connector.Spec{ID: id, TenantID: tenantID, GatewayID: gatewayID,
		DeviceID: in.DeviceID, ConnectorType: in.ConnectorType, TypeVersion: in.TypeVersion,
		Config: append(json.RawMessage(nil), in.Config...), Mappings: append(json.RawMessage(nil), in.Mappings...),
		Revision: 1, ReportedRevision: 1, State: f.state}
	return f.specs[id], nil
}

func (f *fakeConnectors) UpdateSpec(_ context.Context, _ string, specID string, in connector.SaveInput) (connector.Spec, error) {
	f.ensure()
	f.updates++
	if f.calls != nil {
		*f.calls = append(*f.calls, "update:"+in.ConnectorType)
	}
	if f.updateErr != nil {
		err := f.updateErr
		f.updateErr = nil
		return connector.Spec{}, err
	}
	spec, ok := f.specs[specID]
	if !ok {
		return connector.Spec{}, connector.ErrNotFound
	}
	spec.DeviceID = in.DeviceID
	spec.ConnectorType = in.ConnectorType
	spec.TypeVersion = in.TypeVersion
	spec.Config = append(json.RawMessage(nil), in.Config...)
	spec.Mappings = append(json.RawMessage(nil), in.Mappings...)
	spec.Revision++
	spec.ReportedRevision = spec.Revision
	spec.State = f.state
	f.specs[specID] = spec
	return spec, nil
}

func (f *fakeConnectors) GetSpec(_ context.Context, _ string, specID string) (connector.Spec, error) {
	f.ensure()
	spec, ok := f.specs[specID]
	if !ok {
		return connector.Spec{}, connector.ErrNotFound
	}
	spec.State = f.state
	return spec, nil
}

func (f *fakeConnectors) DeleteSpec(_ context.Context, _ string, specID string) error {
	f.ensure()
	f.deletes = append(f.deletes, specID)
	if f.calls != nil {
		*f.calls = append(*f.calls, "delete:"+specID)
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.specs, specID)
	return nil
}

type fakeRules struct {
	groups    int
	drafts    int
	activated bool
	groupIDs  []string
}

func (f *fakeRules) CreateGroup(_ context.Context, _ string, _ string) (string, error) {
	f.groups++
	id := "group-" + string(rune('0'+f.groups))
	f.groupIDs = append(f.groupIDs, id)
	return id, nil
}

func (f *fakeRules) CreateDraft(_ context.Context, _ string, _ string, dto rule.GraphDTO) (string, rule.GraphDTO, error) {
	f.drafts++
	return "chain-" + string(rune('0'+f.drafts)), dto, nil
}

func (f *fakeRules) Activate(context.Context, string, string) error { f.activated = true; return nil }

func (f *fakeRules) SetGroupEnabled(context.Context, string, string, bool) error { return nil }

func (f *fakeRules) DeleteGroup(_ context.Context, _ string, groupID string) error {
	for index, id := range f.groupIDs {
		if id == groupID {
			f.groupIDs = append(f.groupIDs[:index], f.groupIDs[index+1:]...)
			f.groups--
			return nil
		}
	}
	return rule.ErrGroupNotFound
}

type fakeDevices struct {
	devices map[string]registry.DeviceDTO
}

func (f *fakeDevices) ensure() {
	if f.devices == nil {
		f.devices = map[string]registry.DeviceDTO{}
	}
}

func (f *fakeDevices) GetDevice(_ context.Context, _, deviceID string) (registry.DeviceDTO, error) {
	f.ensure()
	device, ok := f.devices[deviceID]
	if !ok {
		device = registry.DeviceDTO{ID: deviceID, CanonicalName: deviceID}
		if strings.HasSuffix(deviceID, "-conclusions-1") {
			device.RuleChainGroupID = "default-group"
		}
		f.devices[deviceID] = device
	}
	return device, nil
}

func (f *fakeDevices) UpdateDevice(_ context.Context, _, deviceID string, input registry.DeviceDTO) (registry.DeviceDTO, error) {
	f.ensure()
	f.devices[deviceID] = input
	return input, nil
}

type fakeMetrics struct {
	metrics   JobMetrics
	available bool
	err       error
}

func (f *fakeMetrics) JobMetrics(context.Context, string, string) (JobMetrics, bool, error) {
	return f.metrics, f.available, f.err
}

type fakeForwarders struct{}

func (fakeForwarders) Get(_ context.Context, tenantID, id string) (egress.Forwarder, error) {
	return egress.Forwarder{ID: id, TenantID: tenantID, Enabled: true}, nil
}

type fakeModels struct{}

func (fakeModels) ListForGateway(context.Context, string, string) ([]modelinventory.Model, error) {
	return []modelinventory.Model{
		{ModelID: "face-detector", Version: "1.0.0", State: modelinventory.StateReady},
		{ModelID: "face-embedding", Version: "1.0.0", State: modelinventory.StateReady},
		{ModelID: "meter-reading-model-1", Version: "1.0.0", State: modelinventory.StateReady},
	}, nil
}

// fakeDistributor is the in-test stand-in for the NereidHorizon ensure owner.
// It records every ensure call and returns a deterministic task handle.
type fakeDistributor struct {
	ensured      []Artifact
	checked      []Artifact
	replay       bool
	adopted      bool
	edgeReady    bool
	err          error
	taskSequence int
}

func (f *fakeDistributor) CheckTarget(artifact Artifact) error {
	f.checked = append(f.checked, artifact)
	if f.err != nil {
		return f.err
	}
	if specString(artifact.Spec, "model_id") == "" ||
		specString(artifact.Spec, "version") == "" || artifact.GatewayID == "" {
		return ErrInvalid
	}
	return nil
}

func (f *fakeDistributor) EnsureDistribution(_ context.Context, _ string, artifact Artifact) (DistributionHandle, error) {
	if f.err != nil {
		return DistributionHandle{}, f.err
	}
	f.ensured = append(f.ensured, artifact)
	f.taskSequence++
	return DistributionHandle{
		TaskID:    fmt.Sprintf("dt-task-%d", f.taskSequence),
		State:     "in_progress",
		EdgeReady: f.edgeReady,
		Replay:    f.replay,
		Adopted:   f.adopted,
	}, nil
}

type fakeManagers struct {
	fakeConnectors
	reason          string
	missingObserved bool
}

func (f *fakeManagers) ensureApplied() {
	if f.state == "" {
		f.state = "APPLIED"
	}
}

func (f *fakeManagers) CreateSpec(ctx context.Context, tenantID, gatewayID string, in connector.SaveInput) (connector.Spec, error) {
	f.ensureApplied()
	return f.fakeConnectors.CreateSpec(ctx, tenantID, gatewayID, in)
}

func (f *fakeManagers) UpdateSpec(ctx context.Context, tenantID, specID string, in connector.SaveInput) (connector.Spec, error) {
	f.ensureApplied()
	return f.fakeConnectors.UpdateSpec(ctx, tenantID, specID, in)
}

func (f *fakeManagers) GetSpec(ctx context.Context, tenantID, specID string) (connector.Spec, error) {
	f.ensureApplied()
	spec, err := f.fakeConnectors.GetSpec(ctx, tenantID, specID)
	spec.ReportReason = f.reason
	if err == nil && spec.ConnectorType == "edge-manager-resource" {
		revision := spec.Revision
		if !f.missingObserved {
			spec.ObservedRevision = &revision
			spec.ObservedHash = "manager-readback-hash"
		}
		spec.ReportedRevision = spec.Revision
	}
	return spec, err
}

type fakeCompanions struct{}

func (fakeCompanions) ApplySubscription(context.Context, string, Artifact) (string, error) {
	return "companion-subscription-1", nil
}

func (fakeCompanions) RemoveSubscription(context.Context, string, Resource) error { return nil }
