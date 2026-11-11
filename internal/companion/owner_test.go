// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/business"
)

// fakeClient records reconcile calls and models companion replies without
// any network. One instance is returned for every endpoint by the test
// factory because each test owns a single endpoint.
type fakeClient struct {
	contractErr error
	putErr      error
	failRowID   string
	checks      int
	putCalls    [][]DesiredRow
}

func (f *fakeClient) CheckContract(context.Context) error {
	f.checks++
	return f.contractErr
}

func (f *fakeClient) PutSubscriptions(_ context.Context, rows []DesiredRow) ([]ReadbackRow, error) {
	f.putCalls = append(f.putCalls, append([]DesiredRow(nil), rows...))
	if f.putErr != nil {
		return nil, f.putErr
	}
	readback := make([]ReadbackRow, 0, len(rows))
	for _, row := range rows {
		status, reason := StatusApplied, ""
		if f.failRowID != "" && row.RowID == f.failRowID {
			status, reason = StatusFailed, "validation-error"
		}
		readback = append(readback, ReadbackRow{
			ID: row.RowID, Revision: 1, Status: status, Reason: reason,
		})
	}
	return readback, nil
}

func newTestOwner(store Store, client CompanionClient) *Owner {
	return NewOwner(store, func(Endpoint) CompanionClient { return client },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func seedEndpoint(t *testing.T, store Store, enabled bool) Endpoint {
	t.Helper()
	ep, err := store.UpsertEndpoint(context.Background(), Endpoint{
		TenantID:        "tenant-1",
		CompanionID:     "face-service",
		GatewayID:       "gateway-1",
		BaseURL:         "http://companion.example",
		ContractVersion: ContractV1,
		AuthToken:       "token-1",
		Enabled:         enabled,
	})
	if err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	return ep
}

func testArtifact(devices any) business.Artifact {
	return business.Artifact{
		Key:       "companion_subscription:face-recognition-v1:face-service:gateway-1",
		Kind:      business.ArtifactKindCompanionSubscription,
		GatewayID: "gateway-1",
		DeviceID:  "face-access-control-conclusions-1",
		Spec: map[string]any{
			"companion_id":         "face-service",
			"conclusion_device_id": "face-access-control-conclusions-1",
			"pipeline_id":          "face-recognition-v1",
			"source":               "PipelineJSON",
			"source_devices":       devices,
		},
	}
}

func TestApplySubscriptionHappyPath(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)

	resourceID, err := owner.ApplySubscription(context.Background(), "tenant-1",
		testArtifact([]string{"cam-2", "cam-1"}))
	if err != nil {
		t.Fatalf("ApplySubscription: %v", err)
	}
	if resourceID != ep.ID {
		t.Fatalf("resource id = %q, want endpoint id %q", resourceID, ep.ID)
	}
	if client.checks != 1 {
		t.Fatalf("contract checks = %d, want 1", client.checks)
	}
	if len(client.putCalls) != 1 {
		t.Fatalf("PUT calls = %d, want 1", len(client.putCalls))
	}
	put := client.putCalls[0]
	if len(put) != 2 {
		t.Fatalf("PUT rows = %d, want 2", len(put))
	}
	if put[0].SourceDevice != "cam-1" || put[1].SourceDevice != "cam-2" {
		t.Fatalf("rows not source-device sorted: %+v", put)
	}
	wantID := "companion:gateway-1:face-access-control-conclusions-1:cam-1"
	if put[0].RowID != wantID {
		t.Fatalf("row id = %q, want %q", put[0].RowID, wantID)
	}
	if put[0].Camera != "cam-1" || put[0].PipelineID != "face-recognition-v1" ||
		!put[0].Enabled || put[0].ConclusionDeviceID != "face-access-control-conclusions-1" {
		t.Fatalf("row wire content mismatch: %+v", put[0])
	}
	rows := store.snapshotRows(ep.ID)
	if len(rows) != 2 {
		t.Fatalf("stored rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Status != StatusApplied || row.Revision != 1 {
			t.Fatalf("read-back not persisted: %+v", row)
		}
	}
}

func TestApplySubscriptionJSONRoundTripShape(t *testing.T) {
	// Expansion snapshots rehydrated from JSONB carry []any, not []string.
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	seedEndpoint(t, store, true)

	if _, err := owner.ApplySubscription(context.Background(), "tenant-1",
		testArtifact([]any{"cam-1", "cam-1", " cam-2 "})); err != nil {
		t.Fatalf("ApplySubscription with []any devices: %v", err)
	}
	put := client.putCalls[0]
	if len(put) != 2 || put[0].SourceDevice != "cam-1" || put[1].SourceDevice != "cam-2" {
		t.Fatalf("dedupe/trim failed: %+v", put)
	}
}

func TestApplySubscriptionValidationFailures(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*business.Artifact)
		setup    func(*testing.T, Store)
		wantText string
	}{
		{
			name:     "no source devices",
			mutate:   func(a *business.Artifact) { a.Spec["source_devices"] = []string{} },
			setup:    func(t *testing.T, s Store) { seedEndpoint(t, s, true) },
			wantText: "source_devices",
		},
		{
			name: "missing companion id",
			mutate: func(a *business.Artifact) {
				delete(a.Spec, "companion_id")
			},
			setup:    func(t *testing.T, s Store) { seedEndpoint(t, s, true) },
			wantText: "requires",
		},
		{
			name:     "endpoint not registered",
			mutate:   func(a *business.Artifact) {},
			setup:    func(t *testing.T, s Store) {},
			wantText: "no companion endpoint registered",
		},
		{
			name:     "endpoint disabled",
			mutate:   func(a *business.Artifact) {},
			setup:    func(t *testing.T, s Store) { seedEndpoint(t, s, false) },
			wantText: "disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			client := &fakeClient{}
			owner := newTestOwner(store, client)
			tc.setup(t, store)
			artifact := testArtifact([]string{"cam-1"})
			tc.mutate(&artifact)
			_, err := owner.ApplySubscription(context.Background(), "tenant-1", artifact)
			if !errors.Is(err, business.ErrInvalid) {
				t.Fatalf("err = %v, want business.ErrInvalid", err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err = %v, want text containing %q", err, tc.wantText)
			}
			if client.checks != 0 || len(client.putCalls) != 0 {
				t.Fatal("validation failure must not contact the companion")
			}
		})
	}
}

func TestContractMismatchMutatesNothing(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{contractErr: ErrContractMismatch}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)

	_, err := owner.ApplySubscription(context.Background(), "tenant-1",
		testArtifact([]string{"cam-1"}))
	if !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("err = %v, want ErrContractMismatch", err)
	}
	if rows := store.snapshotRows(ep.ID); len(rows) != 0 {
		t.Fatalf("contract mismatch left %d desired rows behind", len(rows))
	}
	if len(client.putCalls) != 0 {
		t.Fatal("contract mismatch must not send subscriptions")
	}
}

func TestUpstreamFailureKeepsPreviousCompanionSet(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)

	first := testArtifact([]string{"cam-1"})
	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", first); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	// The companion holds the previously accepted set; the second delivery fails.
	client.putErr = ErrUpstreamError
	second := testArtifact([]string{"cam-1", "cam-2"})
	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", second); err == nil {
		t.Fatal("upstream failure must fail the deployment")
	}
	if len(client.putCalls) != 2 || len(client.putCalls[1]) != 2 {
		t.Fatalf("retry did not attempt the full new desired set: %+v", client.putCalls)
	}
	// Local intent records the new desired state for the next reconcile; the
	// companion-local table keeps the prior set because its PUT failed.
	rows := store.snapshotRows(ep.ID)
	if len(rows) != 2 {
		t.Fatalf("local desired rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.SourceDevice == "cam-2" && row.Status != StatusPending {
			t.Fatalf("new row must stay PENDING after failed delivery: %+v", row)
		}
	}
}

func TestFailedReadbackFailsDeploymentButPersists(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{failRowID: "companion:gateway-1:face-access-control-conclusions-1:cam-2"}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)

	_, err := owner.ApplySubscription(context.Background(), "tenant-1",
		testArtifact([]string{"cam-1", "cam-2"}))
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want rejection error", err)
	}
	rows := store.snapshotRows(ep.ID)
	var failed *DesiredRow
	for i := range rows {
		if rows[i].RowID == client.failRowID {
			failed = &rows[i]
		}
	}
	if failed == nil || failed.Status != StatusFailed || failed.Reason != "validation-error" {
		t.Fatalf("FAILED read-back not persisted: %+v", failed)
	}
}

func TestReApplyUsesDeterministicRowIDs(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)
	artifact := testArtifact([]string{"cam-1"})

	for range 2 {
		if _, err := owner.ApplySubscription(context.Background(), "tenant-1", artifact); err != nil {
			t.Fatalf("re-apply: %v", err)
		}
	}
	if len(client.putCalls) != 2 {
		t.Fatalf("PUT calls = %d, want 2", len(client.putCalls))
	}
	if client.putCalls[0][0].RowID != client.putCalls[1][0].RowID {
		t.Fatal("row ids must stay stable across re-apply")
	}
	if rows := store.snapshotRows(ep.ID); len(rows) != 1 {
		t.Fatalf("re-apply duplicated rows: %d stored", len(rows))
	}
}

func TestMultiArtifactReconcileAndRemove(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	ep := seedEndpoint(t, store, true)

	artifactA := testArtifact([]string{"cam-1", "cam-2"})
	artifactA.Key = "companion_subscription:pipe-a:face-service:gateway-1"
	artifactB := testArtifact([]string{"cam-3"})
	artifactB.Key = "companion_subscription:pipe-b:face-service:gateway-1"

	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", artifactA); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", artifactB); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	lastPut := client.putCalls[len(client.putCalls)-1]
	if len(lastPut) != 3 {
		t.Fatalf("full endpoint set = %d rows, want 3", len(lastPut))
	}

	err := owner.RemoveSubscription(context.Background(), "tenant-1",
		business.Resource{ArtifactKey: artifactA.Key, Kind: business.ArtifactKindCompanionSubscription})
	if err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}
	removePut := client.putCalls[len(client.putCalls)-1]
	if len(removePut) != 1 || removePut[0].SourceDevice != "cam-3" {
		t.Fatalf("post-remove set = %+v, want only cam-3", removePut)
	}
	if rows := store.snapshotRows(ep.ID); len(rows) != 1 || rows[0].ArtifactKey != artifactB.Key {
		t.Fatalf("stored rows after remove = %+v", rows)
	}
}

func TestRemoveLastRowSendsEmptySet(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)
	seedEndpoint(t, store, true)
	artifact := testArtifact([]string{"cam-1"})
	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", artifact); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := owner.RemoveSubscription(context.Background(), "tenant-1",
		business.Resource{ArtifactKey: artifact.Key, Kind: business.ArtifactKindCompanionSubscription}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	lastPut := client.putCalls[len(client.putCalls)-1]
	if len(lastPut) != 0 {
		t.Fatalf("final PUT must carry an empty unsubscribe set, got %d rows", len(lastPut))
	}
}

func TestRemoveIdempotentAndEndpointGone(t *testing.T) {
	store := newMemStore()
	client := &fakeClient{}
	owner := newTestOwner(store, client)

	if err := owner.RemoveSubscription(context.Background(), "tenant-1",
		business.Resource{ArtifactKey: "never:applied", Kind: business.ArtifactKindCompanionSubscription}); err != nil {
		t.Fatalf("remove without rows = %v, want nil", err)
	}
	if len(client.putCalls) != 0 {
		t.Fatal("no-op remove must not call the companion")
	}

	ep := seedEndpoint(t, store, true)
	artifact := testArtifact([]string{"cam-1"})
	if _, err := owner.ApplySubscription(context.Background(), "tenant-1", artifact); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := store.DeleteEndpoint(context.Background(), "tenant-1", ep.ID); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	if err := owner.RemoveSubscription(context.Background(), "tenant-1",
		business.Resource{ArtifactKey: artifact.Key, Kind: business.ArtifactKindCompanionSubscription}); err != nil {
		t.Fatalf("remove after endpoint delete = %v, want nil", err)
	}
}

func TestSpecDevicesAndRowID(t *testing.T) {
	if got := specDevices(map[string]any{"x": []string{"a"}}, "x"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("[]string parse failed: %v", got)
	}
	// Non-string entries are skipped; whitespace is trimmed at parse time.
	if got := specDevices(map[string]any{"x": []any{"a", 1, " b "}}, "x"); len(got) != 2 ||
		got[0] != "a" || got[1] != "b" {
		t.Fatalf("[]any parse failed: %v", got)
	}
	if got := specDevices(map[string]any{}, "x"); got != nil {
		t.Fatalf("missing key = %v, want nil", got)
	}
	if got := RowID("g", "d", "s"); got != "companion:g:d:s" {
		t.Fatalf("RowID = %q", got)
	}
}
