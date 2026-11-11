// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy_test.go covers the deployment <- tenant <- gateway effective-policy
// resolution and the service frame path's per-frame policy lookup.

package discovery

import (
	"context"
	"testing"
)

func boolp(v bool) *bool    { return &v }
func intp(v int) *int       { return &v }
func strp(v string) *string { return &v }

type fakePolicyReader struct {
	tenant  *Record
	gateway *Record
	calls   int
}

func (f *fakePolicyReader) RowsFor(_ context.Context, _, _ string) (*Record, *Record, error) {
	f.calls++
	return f.tenant, f.gateway, nil
}

func TestEffectivePolicyPrecedence(t *testing.T) {
	ctx := context.Background()
	fallback := Policy{Enabled: false, MaxPerCycle: 100, MaxCandidates: 1000, DefaultProfile: "fb"}

	// No reader -> fallback verbatim.
	got, err := EffectivePolicy(ctx, nil, fallback, "t1", "g1")
	if err != nil || got.Enabled || got.MaxPerCycle != 100 || got.DefaultProfile != "fb" {
		t.Fatalf("fallback mismatch: %+v err=%v", got, err)
	}

	// Tenant default enables and changes cap; gateway overrides only the
	// per-cycle cap, inheriting enabled/default profile.
	reader := &fakePolicyReader{
		tenant: &Record{TenantID: "t1", Override: Override{
			Enabled: boolp(true), MaxPerCycle: intp(25), DefaultProfile: strp("ten-prof"),
		}},
		gateway: &Record{TenantID: "t1", GatewayID: "g1", Override: Override{
			MaxPerCycle: intp(5),
		}},
	}
	got, err = EffectivePolicy(ctx, reader, fallback, "t1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("enabled must inherit true from tenant")
	}
	if got.MaxPerCycle != 5 {
		t.Fatalf("gateway cap must win, got %d", got.MaxPerCycle)
	}
	if got.MaxCandidates != 1000 {
		t.Fatalf("max_candidates must inherit fallback, got %d", got.MaxCandidates)
	}
	if got.DefaultProfile != "ten-prof" {
		t.Fatalf("default profile must inherit tenant, got %q", got.DefaultProfile)
	}
}

func TestEffectivePolicyGatewayCanDisable(t *testing.T) {
	ctx := context.Background()
	fallback := Policy{Enabled: true, MaxPerCycle: 100, MaxCandidates: 1000}
	reader := &fakePolicyReader{
		tenant:  &Record{TenantID: "t1", Override: Override{Enabled: boolp(true)}},
		gateway: &Record{TenantID: "t1", GatewayID: "g1", Override: Override{Enabled: boolp(false)}},
	}
	got, err := EffectivePolicy(ctx, reader, fallback, "t1", "g1")
	if err != nil || got.Enabled {
		t.Fatalf("gateway explicit false must disable, got %+v err=%v", got, err)
	}
}

func TestEffectivePolicyFillsCapDefaults(t *testing.T) {
	ctx := context.Background()
	got, err := EffectivePolicy(ctx, nil, Policy{Enabled: true}, "t1", "g1")
	if err != nil || got.MaxPerCycle != 100 || got.MaxCandidates != 1000 {
		t.Fatalf("caps must default: %+v err=%v", got, err)
	}
}

// TestFrameUsesGatewayPolicy verifies HandleDiscovery resolves policy for the
// frame's gateway: a gateway-level disabled override rejects even when the
// deployment fallback is enabled.
func TestFrameUsesGatewayPolicy(t *testing.T) {
	reg := &fakeRegistry{}
	svc := New(reg, nil, Policy{Enabled: true, MaxPerCycle: 100, MaxCandidates: 1000}, nil)
	svc.SetPolicyReader(&fakePolicyReader{
		gateway: &Record{GatewayID: "g1", Override: Override{Enabled: boolp(false)}},
	})
	res, err := svc.HandleDiscovery(context.Background(), "t1", "g1", Frame{
		Candidates: []Candidate{{LocalID: "x"}},
	})
	if err != nil || len(res.Results) != 1 || res.Results[0].Status != StatusRejected {
		t.Fatalf("gateway-disabled policy must reject: %+v err=%v", res, err)
	}
	if len(reg.batch) != 0 {
		t.Fatal("registry must not be called when disabled")
	}
}
