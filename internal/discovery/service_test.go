// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go exercises the transport-independent discovery service with
// a fake registry and Core NATS publisher: the disabled gate, profile
// resolution (default + per-candidate + bad reference), result status mapping,
// the oversize-frame error and advisory emission.

package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

// fakeRegistry records calls and returns scripted provisioning results.
type fakeRegistry struct {
	profiles map[string]string
	result   registry.ProvisionResult
	err      error
	batch    []registry.ProvisionCandidate
	maxCap   int

	classify    []registry.CandidateClassification
	classifyErr error
}

func (f *fakeRegistry) ResolveProfileID(_ context.Context, _, ref string) (string, error) {
	if id, ok := f.profiles[ref]; ok {
		return id, nil
	}
	return "", registry.ErrNotFound
}

func (f *fakeRegistry) AutoProvisionSubdevices(_ context.Context, _, _ string,
	candidates []registry.ProvisionCandidate, maxPerCycle int, _ string) (registry.ProvisionResult, error) {
	f.batch = candidates
	f.maxCap = maxPerCycle
	return f.result, f.err
}

// ClassifyProvisionCandidates returns the scripted classification, or derives
// a "free" view per candidate when none is scripted (review-mode tests).
func (f *fakeRegistry) ClassifyProvisionCandidates(_ context.Context, _, _ string,
	candidates []registry.ProvisionCandidate) ([]registry.CandidateClassification, error) {
	if f.classifyErr != nil {
		return nil, f.classifyErr
	}
	if f.classify != nil {
		return f.classify, nil
	}
	out := make([]registry.CandidateClassification, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, registry.CandidateClassification{
			LocalID: c.LocalID, Name: "gw/" + c.LocalID,
			ProfileID: c.ProfileID, State: registry.CandidateStateFree,
		})
	}
	return out, nil
}

// fakePublisher captures the last advisory.
type fakePublisher struct {
	subject string
	data    []byte
	calls   int
}

func (p *fakePublisher) Publish(subject string, data []byte) error {
	p.subject = subject
	p.data = data
	p.calls++
	return nil
}

// TestDisabledRejectsAll ensures an opt-out policy never reaches the registry
// and returns one rejection per candidate.
func TestDisabledRejectsAll(t *testing.T) {
	reg := &fakeRegistry{}
	svc := New(reg, nil, Policy{Enabled: false}, nil)
	res, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "a"}, {LocalID: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(res.Results))
	}
	for _, r := range res.Results {
		if r.Status != StatusRejected {
			t.Fatalf("want rejected, got %+v", r)
		}
	}
	if reg.batch != nil {
		t.Fatal("registry must not be called when disabled")
	}
}

// TestProvisionMapsResult verifies profile resolution is applied and the
// registry output is translated to created/existing/rejected items.
func TestProvisionMapsResult(t *testing.T) {
	reg := &fakeRegistry{
		profiles: map[string]string{"default-prof": "11111111-1111-1111-1111-111111111111"},
		result: registry.ProvisionResult{
			Provisioned: []registry.ProvisionedDevice{
				{LocalID: "a", DeviceID: "uuid-a", CanonicalName: "gw/a", Existing: false},
				{LocalID: "b", DeviceID: "uuid-b", CanonicalName: "gw/b", Existing: true},
			},
			Rejected: []registry.ProvisionReject{
				{LocalID: "c", Reason: "boom"},
			},
		},
	}
	pub := &fakePublisher{}
	svc := New(reg, pub, Policy{
		Enabled:        true,
		MaxPerCycle:    3,
		MaxCandidates:  10,
		DefaultProfile: "default-prof",
	}, nil)

	res, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Type:       "modbus",
		Candidates: []Candidate{{LocalID: "a"}, {LocalID: "b"}, {LocalID: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.batch) != 3 {
		t.Fatalf("want 3 forwarded candidates, got %d", len(reg.batch))
	}
	if reg.maxCap != 3 {
		t.Fatalf("cap not forwarded: %d", reg.maxCap)
	}
	for _, c := range reg.batch {
		if c.ProfileID != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("default profile not applied: %+v", c)
		}
	}
	status := map[string]string{}
	for _, r := range res.Results {
		status[r.LocalID] = r.Status
	}
	if status["a"] != StatusCreated || status["b"] != StatusExisting || status["c"] != StatusRejected {
		t.Fatalf("unexpected status map: %+v", status)
	}
	if pub.calls != 1 || pub.subject != "nl.discovery.t1.g1" {
		t.Fatalf("advisory not published once: %+v", pub)
	}
}

// TestBadProfileRejectsCandidate ensures an unresolvable per-candidate
// profile rejects only that candidate and never sends it to the registry.
func TestBadProfileRejectsCandidate(t *testing.T) {
	reg := &fakeRegistry{profiles: map[string]string{}}
	svc := New(reg, nil, Policy{Enabled: true, MaxCandidates: 10}, nil)
	res, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "good"}, {LocalID: "bad", Profile: "ghost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.batch) != 1 || reg.batch[0].LocalID != "good" {
		t.Fatalf("only good candidate should be forwarded: %+v", reg.batch)
	}
	if len(res.Results) != 1 {
		t.Fatalf("bad-profile candidate is resolved locally, want 1 result, got %+v", res.Results)
	}
}

// TestMissingDefaultProfileFailsFrame ensures a configured-but-unresolvable
// default profile fails the whole frame instead of creating profile-less rows.
func TestMissingDefaultProfileFailsFrame(t *testing.T) {
	reg := &fakeRegistry{profiles: map[string]string{}}
	svc := New(reg, nil, Policy{Enabled: true, MaxCandidates: 10, DefaultProfile: "missing"}, nil)
	_, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "a"}},
	})
	if err == nil {
		t.Fatal("want error for missing default profile")
	}
}

// TestOversizeFrameRejected ensures the candidate-count cap is enforced.
func TestOversizeFrameRejected(t *testing.T) {
	reg := &fakeRegistry{}
	svc := New(reg, nil, Policy{Enabled: true, MaxCandidates: 2}, nil)
	_, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "a"}, {LocalID: "b"}, {LocalID: "c"}},
	})
	if err == nil {
		t.Fatal("want oversize error")
	}
	if reg.batch != nil {
		t.Fatal("registry must not be called for an oversize frame")
	}
}

// TestRegistryErrorPropagates ensures a failed batch surfaces to the ingress.
func TestRegistryErrorPropagates(t *testing.T) {
	reg := &fakeRegistry{err: errors.New("db down")}
	svc := New(reg, nil, Policy{Enabled: true, MaxCandidates: 10}, nil)
	if _, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "a"}},
	}); err == nil {
		t.Fatal("want propagated error")
	}
}
