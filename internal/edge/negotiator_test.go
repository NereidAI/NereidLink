// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edge

import (
	"testing"
	"time"
)

// TestSelectVersion covers the intersection rule: highest shared
// version, and rejection when majors do not overlap.
func TestSelectVersion(t *testing.T) {
	cases := []struct {
		name   string
		edge   ProtoRange
		server ProtoRange
		want   string
		ok     bool
	}{
		{
			name:   "highest within both",
			edge:   ProtoRange{Min: "1.0", Max: "1.4"},
			server: ProtoRange{Min: "1.0", Max: "2.0"},
			want:   "1.4",
			ok:     true,
		},
		{
			name:   "server caps the ceiling",
			edge:   ProtoRange{Min: "1.0", Max: "3.0"},
			server: ProtoRange{Min: "1.2", Max: "2.0"},
			want:   "2.0",
			ok:     true,
		},
		{
			name:   "intersection floor raised by server",
			edge:   ProtoRange{Min: "1.0", Max: "2.5"},
			server: ProtoRange{Min: "1.3", Max: "2.0"},
			want:   "2.0",
			ok:     true,
		},
		{
			name:   "disjoint majors",
			edge:   ProtoRange{Min: "3.0", Max: "4.0"},
			server: ProtoRange{Min: "1.0", Max: "2.0"},
			want:   "",
			ok:     false,
		},
		{
			name:   "malformed edge version",
			edge:   ProtoRange{Min: "x", Max: "1.0"},
			server: ProtoRange{Min: "1.0", Max: "2.0"},
			want:   "",
			ok:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selectVersion(tc.edge, tc.server)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFeatureIntersection verifies required features are mandatory and
// optional features downgrade away.
func TestFeatureIntersection(t *testing.T) {
	features := FeatureSet{
		Required: []string{"telemetry", "presence"},
		Optional: []string{"ota", "future-thing"},
	}
	got, missing := featureIntersection(features, SupportedFeatures())
	if missing != "" {
		t.Fatalf("unexpected missing feature: %s", missing)
	}
	want := []string{"telemetry", "presence", "ota"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	_, missing = featureIntersection(FeatureSet{
		Required: []string{"telemetry", "nope"},
	}, SupportedFeatures())
	if missing != "nope" {
		t.Fatalf("missing = %q, want nope", missing)
	}
}

// TestNegotiateHappyPath confirms an ok ack selects the highest shared
// version and records a retrievable session.
func TestNegotiateHappyPath(t *testing.T) {
	neg := NewNegotiator(DefaultConfig())
	hello := Hello{
		V:            1,
		EdgeID:       "edge-001",
		AgentVersion: "2.3.1",
		Proto:        ProtoRange{Min: "1.0", Max: "1.4"},
		Features: FeatureSet{
			Required: []string{"telemetry", "presence"},
			Optional: []string{"ota", "shadow"},
		},
		ClientTime: 1727360000123,
	}

	ack, session, err := neg.Negotiate("tenant-1", hello)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if ack.Status != StatusOK {
		t.Fatalf("status = %s (%s), want ok", ack.Status, ack.Reason)
	}
	if ack.Selected != "1.4" {
		t.Fatalf("selected = %s, want 1.4", ack.Selected)
	}
	if len(ack.Features) != 4 {
		t.Fatalf("features = %v, want 4 enabled", ack.Features)
	}
	if session.ID != ack.SessionID {
		t.Fatal("session id does not match ack")
	}
	if _, ok := neg.Session(session.ID); !ok {
		t.Fatal("session not recorded")
	}
}

// TestNegotiateRejections covers the two negotiation failure modes.
func TestNegotiateRejections(t *testing.T) {
	t.Run("incompatible version", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		ack, _, err := neg.Negotiate("tenant-1", Hello{
			V:      1,
			EdgeID: "edge-x",
			Proto:  ProtoRange{Min: "5.0", Max: "6.0"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ack.Status != StatusIncompatibleVersion {
			t.Fatalf("status = %s, want incompatible_version", ack.Status)
		}
	})

	t.Run("incompatible features", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		ack, _, err := neg.Negotiate("tenant-1", Hello{
			V:      1,
			EdgeID: "edge-y",
			Proto:  ProtoRange{Min: "1.0", Max: "2.0"},
			Features: FeatureSet{
				Required: []string{"telemetry", "quantum"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ack.Status != StatusIncompatibleFeatures {
			t.Fatalf("status = %s, want incompatible_features", ack.Status)
		}
	})

	t.Run("missing edge id", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		if _, _, err := neg.Negotiate("tenant-1", Hello{V: 1}); err != ErrInvalidHello {
			t.Fatalf("err = %v, want ErrInvalidHello", err)
		}
	})
}

// TestNegotiateResumeAccepted verifies a reconnect presenting a valid prior
// session id reuses it (accepted=true), while a wrong edge/tenant or an
// expired/unknown id yields a fresh session (accepted=false).
func TestNegotiateResumeAccepted(t *testing.T) {
	baseHello := func(edge string) Hello {
		return Hello{
			V:        1,
			EdgeID:   edge,
			Proto:    ProtoRange{Min: "1.0", Max: "1.4"},
			Features: FeatureSet{Required: []string{"telemetry"}},
		}
	}

	t.Run("same edge resumes within TTL", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.SessionTTL = time.Minute
		neg := NewNegotiator(cfg)
		ack1, _, err := neg.Negotiate("tenant-1", baseHello("edge-r"))
		if err != nil {
			t.Fatalf("first negotiate: %v", err)
		}
		if ack1.Resume.Accepted {
			t.Fatal("fresh hello must not report resume accepted")
		}
		h2 := baseHello("edge-r")
		h2.Resume = ResumeRequest{SessionID: ack1.SessionID}
		ack2, _, err := neg.Negotiate("tenant-1", h2)
		if err != nil {
			t.Fatalf("resume negotiate: %v", err)
		}
		if !ack2.Resume.Accepted || ack2.SessionID != ack1.SessionID {
			t.Fatalf("resume must reuse session, accepted=%v id=%q want %q",
				ack2.Resume.Accepted, ack2.SessionID, ack1.SessionID)
		}
	})

	t.Run("different edge id does not resume", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.SessionTTL = time.Minute
		neg := NewNegotiator(cfg)
		ack1, _, _ := neg.Negotiate("tenant-1", baseHello("edge-a"))
		h2 := baseHello("edge-b")
		h2.Resume = ResumeRequest{SessionID: ack1.SessionID}
		ack2, _, _ := neg.Negotiate("tenant-1", h2)
		if ack2.Resume.Accepted || ack2.SessionID == ack1.SessionID {
			t.Fatal("a foreign edge must not inherit another edge's session")
		}
	})

	t.Run("different tenant does not resume", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.SessionTTL = time.Minute
		neg := NewNegotiator(cfg)
		ack1, _, _ := neg.Negotiate("tenant-1", baseHello("edge-t"))
		h2 := baseHello("edge-t")
		h2.Resume = ResumeRequest{SessionID: ack1.SessionID}
		ack2, _, _ := neg.Negotiate("tenant-2", h2)
		if ack2.Resume.Accepted || ack2.SessionID == ack1.SessionID {
			t.Fatal("a session must not be resumable across tenants")
		}
	})

	t.Run("expired session does not resume", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.SessionTTL = -time.Second
		neg := NewNegotiator(cfg)
		ack1, _, _ := neg.Negotiate("tenant-1", baseHello("edge-e"))
		h2 := baseHello("edge-e")
		h2.Resume = ResumeRequest{SessionID: ack1.SessionID}
		ack2, _, _ := neg.Negotiate("tenant-1", h2)
		if ack2.Resume.Accepted || ack2.SessionID == ack1.SessionID {
			t.Fatal("a disabled-TTL negotiator must never resume")
		}
	})

	t.Run("unknown session id does not resume", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.SessionTTL = time.Minute
		neg := NewNegotiator(cfg)
		h := baseHello("edge-u")
		h.Resume = ResumeRequest{SessionID: "s_doesnotexist"}
		ack, _, err := neg.Negotiate("tenant-1", h)
		if err != nil {
			t.Fatalf("negotiate: %v", err)
		}
		if ack.Resume.Accepted {
			t.Fatal("unknown session id must yield a fresh session")
		}
	})
}

// TestClockSkewWarning verifies skew is a warning bit, never a reject.
func TestClockSkewWarning(t *testing.T) {
	cfg := DefaultConfig()
	neg := NewNegotiator(cfg)
	hello := Hello{
		V:        1,
		EdgeID:   "edge-skew",
		Proto:    ProtoRange{Min: "1.0", Max: "2.0"},
		Features: FeatureSet{Required: []string{"telemetry"}},
		// Far in the past relative to the server clock.
		ClientTime: 1,
	}
	ack, _, err := neg.Negotiate("tenant-1", hello)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ack.Status != StatusOK {
		t.Fatalf("status = %s, skew must not reject", ack.Status)
	}
	if !ack.Time.ClockWarning {
		t.Fatal("expected clock warning for large skew")
	}
}

// TestNegotiateInference covers the ADR-0039 inference capability echo: a
// recognized contract with intersecting backends is accepted and ordered,
// while an unadvertised feature, an unknown contract, or a disjoint backend
// set all leave the block absent (the edge must not enable inference).
func TestNegotiateInference(t *testing.T) {
	base := func() Hello {
		return Hello{
			V: 1, EdgeID: "edge-inf", Proto: ProtoRange{Min: "1.0", Max: "1.4"},
			Features: FeatureSet{
				Required: []string{"telemetry"},
				Optional: []string{FeatureInference},
			},
			Inference: &InferenceAdvert{
				Contract: InferenceContractModel1,
				Backends: []string{"openai", "kserve", "vendor-x"},
			},
		}
	}

	t.Run("accepted with ordered backend intersection", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		ack, _, err := neg.Negotiate("tenant-1", base())
		if err != nil || ack.Status != StatusOK {
			t.Fatalf("negotiate: %v status=%s", err, ack.Status)
		}
		if !contains(ack.Features, FeatureInference) {
			t.Fatalf("inference feature missing from %v", ack.Features)
		}
		if ack.Inference == nil {
			t.Fatal("expected echoed inference block")
		}
		if ack.Inference.Contract != InferenceContractModel1 {
			t.Fatalf("contract = %q", ack.Inference.Contract)
		}
		// Gateway order preserved; unknown backend dropped.
		if got := ack.Inference.Backends; len(got) != 2 ||
			got[0] != "openai" || got[1] != "kserve" {
			t.Fatalf("backends = %v, want [openai kserve]", got)
		}
	})

	t.Run("feature not advertised stays disabled", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		h := base()
		h.Features.Optional = nil
		ack, _, _ := neg.Negotiate("tenant-1", h)
		if ack.Inference != nil {
			t.Fatalf("inference must be nil without the feature: %+v", ack.Inference)
		}
	})

	t.Run("unknown contract stays disabled", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		h := base()
		h.Inference.Contract = "model/9"
		ack, _, _ := neg.Negotiate("tenant-1", h)
		if ack.Inference != nil {
			t.Fatalf("inference must be nil for an unknown contract: %+v", ack.Inference)
		}
	})

	t.Run("disjoint backends stay disabled", func(t *testing.T) {
		neg := NewNegotiator(DefaultConfig())
		h := base()
		h.Inference.Backends = []string{"vendor-x"}
		ack, _, _ := neg.Negotiate("tenant-1", h)
		if ack.Inference != nil {
			t.Fatalf("inference must be nil with no common backend: %+v", ack.Inference)
		}
	})
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
