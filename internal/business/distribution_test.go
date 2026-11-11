// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package business

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/connector"
)

func fullOwners(distributor ModelDistributor) Owners {
	return Owners{
		Connectors:   &fakeConnectors{state: "APPLIED"},
		Rules:        &fakeRules{},
		Devices:      &fakeDevices{},
		Forwarders:   &fakeForwarders{},
		Models:       &fakeModels{},
		Managers:     &fakeManagers{},
		Companions:   &fakeCompanions{},
		Metrics:      &fakeMetrics{},
		Distribution: distributor,
	}
}

func TestPreflightBlocksWhenModelDistributionDisabled(t *testing.T) {
	store := newMemoryStore()
	// Every other owner is present; only the NH distributor is missing.
	service := NewService(store, Owners{
		Connectors: &fakeConnectors{state: "APPLIED"},
		Rules:      &fakeRules{},
		Devices:    &fakeDevices{},
		Forwarders: &fakeForwarders{},
		Models:     &fakeModels{},
		Managers:   &fakeManagers{},
		Companions: &fakeCompanions{},
		Metrics:    &fakeMetrics{},
	}, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	report, err := service.ValidateInstance(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !hasIssue(report, "model_distribution_unavailable") {
		t.Fatalf("expected disabled-distribution blocker, got valid=%v issues=%+v", report.Valid, report.Issues)
	}
	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deploy must fail before side effects, got %v", err)
	}
}

func TestPreflightBlocksInvalidDistributionTarget(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, fullOwners(&fakeDistributor{
		err: errors.New("gateway gateway-1 has no ESS destination mapping"),
	}), nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	report, err := service.ValidateInstance(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || !hasIssue(report, "model_target_invalid") {
		t.Fatalf("expected target blocker, got %+v", report.Issues)
	}
}

func TestDeployEnsuresModelDistributionAndPersistsTaskHandle(t *testing.T) {
	store := newMemoryStore()
	distributor := &fakeDistributor{}
	service := NewService(store, fullOwners(distributor), nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	deployed, err := service.Deploy(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// The face template declares two model nodes, each ensured once on the
	// single target gateway.
	if len(distributor.ensured) != 2 {
		t.Fatalf("expected 2 ensure calls, got %d", len(distributor.ensured))
	}
	for _, artifact := range distributor.ensured {
		if artifact.Kind != ArtifactKindModelDistribution || artifact.GatewayID == "" {
			t.Fatalf("unexpected ensured artifact: %+v", artifact)
		}
		if specString(artifact.Spec, "model_id") == "" || specString(artifact.Spec, "version") == "" {
			t.Fatalf("ensured artifact lost its model identity: %+v", artifact.Spec)
		}
	}

	snapshot, err := store.GetExpansion(context.Background(), "tenant", instance.ID, deployed.ActiveGeneration)
	if err != nil {
		t.Fatal(err)
	}
	var modelResources []Resource
	ownerIDs := map[string]bool{}
	for _, resource := range snapshot.Resources {
		if resource.Kind == ArtifactKindModelDistribution {
			modelResources = append(modelResources, resource)
			ownerIDs[resource.OwnerID] = true
			if resource.ResourceID == "" {
				t.Fatal("NH task id was not persisted as the resource handle")
			}
		}
	}
	if len(modelResources) != 2 {
		t.Fatalf("expected 2 model resources, got %d", len(modelResources))
	}
	if !ownerIDs["face-detector@1.0.0"] || !ownerIDs["face-embedding@1.0.0"] {
		t.Fatalf("unexpected owner identities: %+v", ownerIDs)
	}
}

type applyFailingDistributor struct {
	calls int
}

func (applyFailingDistributor) CheckTarget(Artifact) error { return nil }

func (d *applyFailingDistributor) EnsureDistribution(context.Context, string, Artifact) (DistributionHandle, error) {
	d.calls++
	return DistributionHandle{}, connector.ErrNotFound
}

func TestModelDistributionApplyFailureFailsDeployment(t *testing.T) {
	store := newMemoryStore()
	distributor := &applyFailingDistributor{}
	service := NewService(store, fullOwners(distributor), nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	if _, err := service.Deploy(context.Background(), "tenant", instance.ID); err == nil {
		t.Fatal("NH ensure failure must fail the deployment")
	}
	if distributor.calls == 0 {
		t.Fatal("distribution owner was never invoked")
	}
}

func TestModelDistributionNoLongerDependsOnEdgeInventoryReadiness(t *testing.T) {
	// Preflight passes with the distributor even when the inventory reader is
	// absent: READY is an observed health signal, not a deployment gate.
	store := newMemoryStore()
	owners := fullOwners(&fakeDistributor{})
	owners.Models = nil
	service := NewService(store, owners, nil)
	template, input := validTemplateInput(t, "face-access-control")
	instance := createTestInstance(t, service, template, input)

	report, err := service.ValidateInstance(context.Background(), "tenant", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range report.Issues {
		if issue.Code == "model_inventory_unavailable" || issue.Code == "model_not_ready" {
			t.Fatalf("inventory readiness must no longer gate distribution: %+v", issue)
		}
	}
}
