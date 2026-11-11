// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package horizon

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/business"
	"github.com/nereidai/nereidlink/internal/config"
)

type fakeTaskClient struct {
	requests []EnsureRequest
	tasks    []Task
	err      error
}

func (f *fakeTaskClient) EnsureTask(_ context.Context, req EnsureRequest) (Task, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return Task{}, f.err
	}
	if len(f.tasks) > 0 {
		return f.tasks[len(f.requests)-1], nil
	}
	return Task{TaskID: "dt_seed", State: StateInProgress, ObjectID: req.ObjectID}, nil
}

func (f *fakeTaskClient) GetTask(_ context.Context, _ string) (Task, error) {
	return Task{}, nil
}

var _ TaskClient = (*fakeTaskClient)(nil)

func baseOwner(t *testing.T, client TaskClient, section config.HorizonSection) *Owner {
	t.Helper()
	if section.GatewayDestinations == nil {
		section.GatewayDestinations = map[string]string{
			"gateway-1": "openhorizon.edgenode:node1",
		}
	}
	owner, err := NewOwner(section, client, nil)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	return owner
}

func modelArtifact(overrides map[string]any) business.Artifact {
	spec := map[string]any{
		"model_id": "face-detector",
		"version":  "1.0.0",
	}
	for k, v := range overrides {
		spec[k] = v
	}
	return business.Artifact{
		Key: "model_distribution:face-detector:gateway-1", Kind: business.ArtifactKindModelDistribution,
		GatewayID: "gateway-1", Spec: spec,
	}
}

func TestOwnerPlansDefaultEnvelopeDeterministically(t *testing.T) {
	client := &fakeTaskClient{}
	owner := baseOwner(t, client, config.HorizonSection{})

	handle, err := owner.EnsureDistribution(context.Background(), "tenant", modelArtifact(nil))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if handle.TaskID != "dt_seed" || handle.Replay || handle.Adopted {
		t.Fatalf("unexpected handle: %+v", handle)
	}
	req := client.requests[0]
	if req.ModelName != "face-detector" || req.Version != "1.0.0" {
		t.Fatalf("forge identity not forwarded: %+v", req)
	}
	if req.Precision != PrecisionFP16 || req.ObjectType != DefaultObjectType {
		t.Fatalf("defaults not applied: %+v", req)
	}
	if req.ObjectID != "face-detector-1.0.0-fp16" {
		t.Fatalf("derived object id not deterministic: %q", req.ObjectID)
	}
	if len(req.Destinations) != 1 || req.Destinations[0] != "openhorizon.edgenode:node1" {
		t.Fatalf("destination mapping wrong: %+v", req.Destinations)
	}
}

func TestOwnerRepeatedEnsureConvergesOnSameIdempotencyKey(t *testing.T) {
	client := &fakeTaskClient{}
	owner := baseOwner(t, client, config.HorizonSection{})
	artifact := modelArtifact(nil)

	for i := 0; i < 3; i++ {
		if _, err := owner.EnsureDistribution(context.Background(), "tenant", artifact); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 ensure calls, got %d", len(client.requests))
	}
	first := client.requests[0]
	for _, req := range client.requests[1:] {
		if req.ObjectID != first.ObjectID || req.ObjectType != first.ObjectType {
			t.Fatalf("retry diverged from the idempotency key: %+v vs %+v", first, req)
		}
	}
}

func TestOwnerHonorsExplicitAssemblyOverrides(t *testing.T) {
	client := &fakeTaskClient{}
	section := config.HorizonSection{DefaultPrecision: "int8"}
	owner := baseOwner(t, client, section)

	artifact := modelArtifact(map[string]any{
		"precision":   "fp16",
		"object_type": "face-package",
		"object_id":   "yunet-fp16",
	})
	if err := owner.CheckTarget(artifact); err != nil {
		t.Fatalf("check target: %v", err)
	}
	if _, err := owner.EnsureDistribution(context.Background(), "tenant", artifact); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	req := client.requests[0]
	if req.Precision != "fp16" {
		t.Fatalf("explicit precision must beat the section default: %q", req.Precision)
	}
	if req.ObjectType != "face-package" || req.ObjectID != "yunet-fp16" {
		t.Fatalf("explicit ESS identity not honored: %+v", req)
	}
}

func TestOwnerCheckTargetBlocksUnmappedGatewayAndBadPrecision(t *testing.T) {
	owner := baseOwner(t, &fakeTaskClient{}, config.HorizonSection{})

	unmapped := modelArtifact(nil)
	unmapped.GatewayID = "gateway-unknown"
	if err := owner.CheckTarget(unmapped); err == nil {
		t.Fatal("unmapped gateway must block preflight")
	}

	badPrecision := modelArtifact(map[string]any{"precision": "int4"})
	if err := owner.CheckTarget(badPrecision); err == nil {
		t.Fatal("invalid precision must block preflight")
	}

	missingIdentity := modelArtifact(nil)
	delete(missingIdentity.Spec, "model_id")
	if err := owner.CheckTarget(missingIdentity); err == nil {
		t.Fatal("missing model identity must block preflight")
	}
}

func TestOwnerSanitizesUnsafeDerivedObjectID(t *testing.T) {
	client := &fakeTaskClient{}
	owner := baseOwner(t, client, config.HorizonSection{})
	artifact := modelArtifact(map[string]any{"model_id": "face/detector space", "version": "1/0"})

	if _, err := owner.EnsureDistribution(context.Background(), "tenant", artifact); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	req := client.requests[0]
	if !safeToken(req.ObjectID) {
		t.Fatalf("derived object id %q contains unsafe runes", req.ObjectID)
	}
	if req.ObjectID != "face-detector-space-1-0-fp16" {
		t.Fatalf("unexpected sanitized object id %q", req.ObjectID)
	}
}

func TestOwnerRejectsUnsafeExplicitIdentity(t *testing.T) {
	owner := baseOwner(t, &fakeTaskClient{}, config.HorizonSection{})
	artifact := modelArtifact(map[string]any{"object_id": "bad/id"})
	if err := owner.CheckTarget(artifact); err == nil {
		t.Fatal("explicit object id with a path separator must be rejected")
	}
}

func TestOwnerPropagatesUpstreamFailure(t *testing.T) {
	client := &fakeTaskClient{err: errors.New("connection refused")}
	owner := baseOwner(t, client, config.HorizonSection{})
	if _, err := owner.EnsureDistribution(context.Background(), "tenant", modelArtifact(nil)); err == nil {
		t.Fatal("upstream failure must fail the deployment")
	}
}

func TestOwnerRejectsEmptyTaskID(t *testing.T) {
	client := &fakeTaskClient{tasks: []Task{{TaskID: ""}}}
	owner := baseOwner(t, client, config.HorizonSection{})
	if _, err := owner.EnsureDistribution(context.Background(), "tenant", modelArtifact(nil)); err == nil {
		t.Fatal("empty NH task id must be rejected")
	}
}
