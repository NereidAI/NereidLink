// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// approval_test.go covers the human-review branch of discovery: candidates
// are queued (status pending), existing matches resolve immediately, the
// per-cycle cap bounds new queue entries, and approve/reject drive the
// registry + decision dispatch.

package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

// fakeCandidateStore is an in-memory CandidateLister.
type fakeCandidateStore struct {
	pending   map[string]CandidateRecord
	duplicate bool
	approved  []string
	rejected  []string
}

func newFakeCandidateStore() *fakeCandidateStore {
	return &fakeCandidateStore{pending: map[string]CandidateRecord{}}
}

func (f *fakeCandidateStore) PendingInsert(_ context.Context, in NewCandidate) (CandidateRecord, error) {
	if f.duplicate {
		return CandidateRecord{}, ErrPendingExists
	}
	if _, ok := f.pending[in.LocalID]; ok {
		return CandidateRecord{}, ErrPendingExists
	}
	rec := CandidateRecord{
		ID: "cand-" + in.LocalID, TenantID: in.TenantID, GatewayID: in.GatewayID,
		LocalID: in.LocalID, CandidateName: in.CandidateName,
		ProfileID: in.ProfileID, State: StatePending,
	}
	f.pending[in.LocalID] = rec
	return rec, nil
}

func (f *fakeCandidateStore) ListCandidates(_ context.Context, _, _, _ string, _, _ int) ([]CandidateRecord, int, error) {
	out := make([]CandidateRecord, 0, len(f.pending))
	for _, c := range f.pending {
		out = append(out, c)
	}
	return out, len(out), nil
}

func (f *fakeCandidateStore) GetByID(_ context.Context, _, id string) (CandidateRecord, error) {
	for _, c := range f.pending {
		if c.ID == id {
			return c, nil
		}
	}
	return CandidateRecord{}, ErrCandidateNotFound
}

func (f *fakeCandidateStore) GetPendingCandidate(ctx context.Context, tenant, id string) (CandidateRecord, error) {
	c, err := f.GetByID(ctx, tenant, id)
	if err != nil {
		return CandidateRecord{}, err
	}
	if c.State != StatePending {
		return c, ErrCandidateNotPending
	}
	return c, nil
}

func (f *fakeCandidateStore) MarkApproved(_ context.Context, _, id, deviceID, _, _ string) error {
	for k, c := range f.pending {
		if c.ID == id {
			c.State = StateApproved
			c.DeviceID = deviceID
			f.pending[k] = c
			f.approved = append(f.approved, id)
			return nil
		}
	}
	return ErrCandidateNotFound
}

func (f *fakeCandidateStore) MarkRejected(_ context.Context, _, id, _, _ string) error {
	for k, c := range f.pending {
		if c.ID == id {
			c.State = StateRejected
			f.pending[k] = c
			f.rejected = append(f.rejected, id)
			return nil
		}
	}
	return ErrCandidateNotFound
}

// fakeLocator returns a scripted ingress location.
type fakeLocator struct {
	found     bool
	ingressID string
}

func (f fakeLocator) LookupGateway(_ context.Context, _, _ string) (GatewayLocation, error) {
	return GatewayLocation{Found: f.found, IngressID: f.ingressID}, nil
}

// TestReviewQueuesFreeCandidates verifies review mode parks free candidates
// as pending while still resolving existing matches now.
func TestReviewQueuesFreeCandidates(t *testing.T) {
	reg := &fakeRegistry{
		classify: []registry.CandidateClassification{
			{LocalID: "new-1", Name: "gw/new-1", State: registry.CandidateStateFree},
			{LocalID: "old", Name: "gw/old", DeviceID: "dev-old",
				ProfileID: "prof", State: registry.CandidateStateExisting},
			{LocalID: "clash", Name: "gw/clash",
				Reason: "canonical name already used by another device", State: registry.CandidateStateConflict},
		},
	}
	svc := New(reg, &fakePublisher{}, Policy{Enabled: true, ApprovalMode: ApprovalReview}, nil)
	store := newFakeCandidateStore()
	svc.SetCandidateStore(store)

	res, err := svc.HandleDiscovery(context.Background(), "t1", "gw1", Frame{
		Candidates: []Candidate{
			{LocalID: "new-1"}, {LocalID: "old"}, {LocalID: "clash"},
		},
	})
	if err != nil {
		t.Fatalf("HandleDiscovery: %v", err)
	}
	byLocal := map[string]ItemResult{}
	for _, r := range res.Results {
		byLocal[r.LocalID] = r
	}
	if byLocal["new-1"].Status != StatusPending {
		t.Fatalf("new-1 want pending, got %q", byLocal["new-1"].Status)
	}
	if byLocal["old"].Status != StatusExisting || byLocal["old"].DeviceID != "dev-old" {
		t.Fatalf("old must resolve existing now: %+v", byLocal["old"])
	}
	if byLocal["clash"].Status != StatusRejected {
		t.Fatalf("clash want rejected, got %q", byLocal["clash"].Status)
	}
	if len(store.pending) != 1 {
		t.Fatalf("want 1 pending row, got %d", len(store.pending))
	}
}

// TestReviewCapBoundsQueue verifies the per-cycle cap bounds new pending
// inserts but existing matches do not count toward it.
func TestReviewCapBoundsQueue(t *testing.T) {
	reg := &fakeRegistry{} // every candidate classifies free
	svc := New(reg, &fakePublisher{}, Policy{Enabled: true, ApprovalMode: ApprovalReview, MaxPerCycle: 2}, nil)
	svc.SetCandidateStore(newFakeCandidateStore())

	res, err := svc.HandleDiscovery(context.Background(), "t1", "gw1", Frame{
		Candidates: []Candidate{{LocalID: "a"}, {LocalID: "b"}, {LocalID: "c"}},
	})
	if err != nil {
		t.Fatalf("HandleDiscovery: %v", err)
	}
	pending, rejected := 0, 0
	for _, r := range res.Results {
		switch r.Status {
		case StatusPending:
			pending++
		case StatusRejected:
			rejected++
		}
	}
	if pending != 2 || rejected != 1 {
		t.Fatalf("want 2 pending + 1 cap rejection, got %d/%d", pending, rejected)
	}
}

// TestApproveCreatesAndDispatches verifies approval creates via the registry
// and dispatches the result to the owning ingress.
func TestApproveCreatesAndDispatches(t *testing.T) {
	reg := &fakeRegistry{result: registry.ProvisionResult{
		Provisioned: []registry.ProvisionedDevice{
			{LocalID: "x", DeviceID: "dev-x", CanonicalName: "gw/x"},
		},
	}}
	pub := &fakePublisher{}
	svc := New(reg, pub, Policy{Enabled: true, ApprovalMode: ApprovalReview}, nil)
	store := newFakeCandidateStore()
	svc.SetCandidateStore(store)
	svc.SetLocationResolver(fakeLocator{found: true, ingressID: "ing-1"})

	if _, err := store.PendingInsert(context.Background(), NewCandidate{
		TenantID: "t1", GatewayID: "gw1", LocalID: "x", CandidateName: "gw/x",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cand, item, err := svc.ApproveCandidate(context.Background(), "t1", "cand-x", "user-1")
	if err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}
	if cand.State != StateApproved || item.DeviceID != "dev-x" || item.Status != StatusCreated {
		t.Fatalf("bad approval: %+v %+v", cand, item)
	}
	if pub.subject != DecisionDispatchSubjectPrefix+"ing-1" {
		t.Fatalf("dispatch subject want %s, got %q", DecisionDispatchSubjectPrefix+"ing-1", pub.subject)
	}
}

// TestApproveRejectedByRegistry verifies a registry refusal on approval marks
// the candidate REJECTED and returns ErrApprovalRejected.
func TestApproveRejectedByRegistry(t *testing.T) {
	reg := &fakeRegistry{result: registry.ProvisionResult{
		Rejected: []registry.ProvisionReject{{LocalID: "x", Reason: "quota exceeded"}},
	}}
	svc := New(reg, &fakePublisher{}, Policy{Enabled: true, ApprovalMode: ApprovalReview}, nil)
	store := newFakeCandidateStore()
	svc.SetCandidateStore(store)
	if _, err := store.PendingInsert(context.Background(), NewCandidate{
		TenantID: "t1", GatewayID: "gw1", LocalID: "x",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, _, err := svc.ApproveCandidate(context.Background(), "t1", "cand-x", "user-1")
	if !errors.Is(err, ErrApprovalRejected) {
		t.Fatalf("want ErrApprovalRejected, got %v", err)
	}
	c, _ := store.GetByID(context.Background(), "t1", "cand-x")
	if c.State != StateRejected {
		t.Fatalf("candidate want REJECTED, got %s", c.State)
	}
}

// TestDecideNonPendingErrors verifies a second decision conflicts.
func TestDecideNonPendingErrors(t *testing.T) {
	reg := &fakeRegistry{result: registry.ProvisionResult{
		Provisioned: []registry.ProvisionedDevice{{LocalID: "x", DeviceID: "d"}},
	}}
	svc := New(reg, &fakePublisher{}, Policy{Enabled: true, ApprovalMode: ApprovalReview}, nil)
	store := newFakeCandidateStore()
	svc.SetCandidateStore(store)
	rec, _ := store.PendingInsert(context.Background(), NewCandidate{TenantID: "t1", GatewayID: "gw1", LocalID: "x"})
	if _, err := svc.RejectCandidate(context.Background(), "t1", rec.ID, "u", ""); err != nil {
		t.Fatalf("first reject: %v", err)
	}
	if _, err := svc.RejectCandidate(context.Background(), "t1", rec.ID, "u", ""); !errors.Is(err, ErrCandidateNotPending) {
		t.Fatalf("second reject want ErrCandidateNotPending, got %v", err)
	}
}
