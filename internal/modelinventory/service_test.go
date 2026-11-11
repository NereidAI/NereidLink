// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package modelinventory

import (
	"context"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

func TestInputValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		ok   bool
	}{
		{"ready", Input{ID: "m", Version: "1", State: StateReady}, true},
		{"failed", Input{ID: "m", Version: "1", State: StateFailed}, true},
		{"missing id", Input{Version: "1", State: StateReady}, false},
		{"missing version", Input{ID: "m", State: StateReady}, false},
		{"bad state", Input{ID: "m", Version: "1", State: "NOPE"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if c.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want invalid, got nil")
			}
		})
	}
}

func TestRuntimeClassify(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		snap RuntimeSnapshot
		want string
	}{
		{"missing block", RuntimeSnapshot{Present: false}, ""},
		{"fresh online", RuntimeSnapshot{Present: true, State: RuntimeOnline,
			CheckedAt: now.Add(-10 * time.Second), ProbeIntervalS: 30}, RuntimeOnline},
		{"fresh degraded stays degraded", RuntimeSnapshot{Present: true, State: RuntimeDegraded,
			CheckedAt: now.Add(-20 * time.Second), ProbeIntervalS: 30}, RuntimeDegraded},
		{"explicit offline is offline regardless of freshness", RuntimeSnapshot{
			Present: true, State: RuntimeOffline, CheckedAt: now, ProbeIntervalS: 30}, RuntimeOffline},
		{"silent past 3x probe is unknown", RuntimeSnapshot{Present: true, State: RuntimeOnline,
			CheckedAt: now.Add(-100 * time.Second), ProbeIntervalS: 30}, RuntimeUnknown},
		{"no probe interval cannot prove silence", RuntimeSnapshot{Present: true, State: RuntimeOnline,
			CheckedAt: time.Time{}, ProbeIntervalS: 0}, RuntimeOnline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.snap.classifyRuntime(now); got != c.want {
				t.Fatalf("classify = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeterministicUUIDShape(t *testing.T) {
	id := deterministicUUID("tenant-1", ":device-profile:detection/v1")
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("not a canonical uuid: %q", id)
	}
	// Same inputs converge; different inputs diverge.
	if deterministicUUID("tenant-1", ":device-profile:detection/v1") != id {
		t.Fatal("deterministic uuid not stable")
	}
	if deterministicUUID("tenant-2", ":device-profile:detection/v1") == id {
		t.Fatal("uuid must vary with tenant")
	}
}

// ---- service fakes ----

type fakeStore struct {
	exists       bool
	reconcile    ReconcileResult
	stale        bool
	failed       []Model
	runtimeRows  []RuntimeRow
	reconcileIn  []Input
	markedTenant string
	markedGW     string
	segmentStale []GatewayRef
}

func (f *fakeStore) GatewayExists(context.Context, string, string) (bool, error) {
	return f.exists, nil
}
func (f *fakeStore) Reconcile(_ context.Context, _, _ string, in []Input, _ time.Time) (ReconcileResult, error) {
	f.reconcileIn = in
	return f.reconcile, nil
}
func (f *fakeStore) ListByGateway(context.Context, string, string) ([]Model, error) {
	return nil, nil
}
func (f *fakeStore) ClaimFailedForRaise(context.Context) ([]Model, error) {
	return f.failed, nil
}
func (f *fakeStore) MarkStale(_ context.Context, tenant, gw string, _ time.Time) (bool, error) {
	f.markedTenant, f.markedGW = tenant, gw
	return f.stale, nil
}
func (f *fakeStore) MarkInventoryStale(context.Context, time.Time, time.Time) ([]GatewayRef, error) {
	return f.segmentStale, nil
}
func (f *fakeStore) ListRuntimeBlocks(context.Context) ([]RuntimeRow, error) {
	return f.runtimeRows, nil
}

type fakeAlarms struct {
	created []alarm.CreateInput
	cleared []string
}

func (f *fakeAlarms) CreateAlarm(_ context.Context, in alarm.CreateInput) (alarm.Outcome, error) {
	f.created = append(f.created, in)
	return alarm.Outcome{Created: true}, nil
}
func (f *fakeAlarms) ClearAlarm(_ context.Context, _, _ string, key string) (alarm.Outcome, error) {
	f.cleared = append(f.cleared, key)
	// Report an actual transition so the service publishes a recovery event.
	return alarm.Outcome{AlarmID: "a1", State: alarm.StateCleared}, nil
}

func newSvc(store Store, alarms AlarmRaiser) *Service {
	return NewService(nil, store, alarms, 0, nil)
}

func TestReconcileDerivesRecoveryAlarms(t *testing.T) {
	st := &fakeStore{
		exists: true,
		reconcile: ReconcileResult{
			WasStale: true,
			ClearedFailed: []Model{
				{TenantID: "t", GatewayID: "g", ModelID: "m1", Version: "1"},
			},
			Upserted: []Model{{ModelID: "m1", Version: "1"}},
		},
	}
	al := &fakeAlarms{}
	svc := newSvc(st, al)

	in := []Input{{ID: "m1", Version: "1", State: StateReady}}
	if _, err := svc.Reconcile(context.Background(), "t", "g", in, time.Now()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	wantCleared := map[string]bool{
		DedupeModelFailedKey("g", "m1", "1"): false,
		DedupeModelStaleKey("g"):             false,
	}
	for _, key := range al.cleared {
		if _, ok := wantCleared[key]; ok {
			wantCleared[key] = true
		}
	}
	for key, got := range wantCleared {
		if !got {
			t.Errorf("expected clear of %q, clears=%v", key, al.cleared)
		}
	}
	if len(st.reconcileIn) != 1 || st.reconcileIn[0].ID != "m1" {
		t.Fatalf("reported inputs not forwarded: %+v", st.reconcileIn)
	}
}

func TestReconcileRejectsUnknownGateway(t *testing.T) {
	svc := newSvc(&fakeStore{exists: false}, &fakeAlarms{})
	if _, err := svc.Reconcile(context.Background(), "t", "g", nil, time.Now()); err != ErrGatewayNotFound {
		t.Fatalf("want ErrGatewayNotFound, got %v", err)
	}
}

func TestOnManifestLeaseExpired(t *testing.T) {
	t.Run("newly stale raises one gateway alarm", func(t *testing.T) {
		st := &fakeStore{stale: true}
		al := &fakeAlarms{}
		svc := newSvc(st, al)
		svc.OnManifestLeaseExpired(context.Background(), "t", "g")
		if len(al.created) != 1 || al.created[0].AlarmType != AlarmTypeModelStale {
			t.Fatalf("want one stale alarm, got %+v", al.created)
		}
		if st.markedGW != "g" {
			t.Fatal("store MarkStale not called")
		}
	})
	t.Run("no new stale rows is a no-op", func(t *testing.T) {
		al := &fakeAlarms{}
		svc := newSvc(&fakeStore{stale: false}, al)
		svc.OnManifestLeaseExpired(context.Background(), "t", "g")
		if len(al.created) != 0 {
			t.Fatalf("want no alarm, got %+v", al.created)
		}
	})
}

func TestEvaluateRuntime(t *testing.T) {
	now := time.Now().UTC()
	t.Run("offline raises major", func(t *testing.T) {
		al := &fakeAlarms{}
		svc := newSvc(&fakeStore{}, al)
		svc.evaluateRuntime(context.Background(), RuntimeRow{
			TenantID: "t", GatewayID: "g",
			Block: map[string]any{
				"state": "OFFLINE", "checked_at": now.Format(time.RFC3339),
				"probe_interval_s": 30,
			},
		}, now)
		if len(al.created) != 1 || al.created[0].Severity != alarm.SeverityMajor {
			t.Fatalf("want one MAJOR runtime alarm, got %+v", al.created)
		}
	})
	t.Run("silent block raises warning unknown", func(t *testing.T) {
		al := &fakeAlarms{}
		svc := newSvc(&fakeStore{}, al)
		svc.evaluateRuntime(context.Background(), RuntimeRow{
			TenantID: "t", GatewayID: "g",
			Block: map[string]any{
				"state": "ONLINE", "checked_at": now.Add(-200 * time.Second).Format(time.RFC3339),
				"probe_interval_s": 30,
			},
		}, now)
		if len(al.created) != 1 || al.created[0].Severity != alarm.SeverityWarning ||
			al.created[0].Details["state"] != RuntimeUnknown {
			t.Fatalf("want one WARNING UNKNOWN alarm, got %+v", al.created)
		}
	})
	t.Run("fresh online clears", func(t *testing.T) {
		al := &fakeAlarms{}
		svc := newSvc(&fakeStore{}, al)
		svc.evaluateRuntime(context.Background(), RuntimeRow{
			TenantID: "t", GatewayID: "g",
			Block: map[string]any{
				"state": "ONLINE", "checked_at": now.Add(-5 * time.Second).Format(time.RFC3339),
				"probe_interval_s": 30,
			},
		}, now)
		if len(al.created) != 0 || len(al.cleared) != 1 || al.cleared[0] != DedupeRuntimeKey("g") {
			t.Fatalf("want runtime clear, created=%v cleared=%v", al.created, al.cleared)
		}
	})
	t.Run("missing block skips", func(t *testing.T) {
		al := &fakeAlarms{}
		svc := newSvc(&fakeStore{}, al)
		svc.evaluateRuntime(context.Background(), RuntimeRow{TenantID: "t", GatewayID: "g"}, now)
		if len(al.created) != 0 || len(al.cleared) != 0 {
			t.Fatalf("missing block must be inert, got %+v/%v", al.created, al.cleared)
		}
	})
}

func TestSweepRaisesSegmentSilentStale(t *testing.T) {
	st := &fakeStore{
		segmentStale: []GatewayRef{{TenantID: "t", GatewayID: "g"}},
	}
	al := &fakeAlarms{}
	svc := newSvc(st, al)
	svc.sweep(context.Background(), time.Now().UTC())
	if len(al.created) != 1 || al.created[0].AlarmType != AlarmTypeModelStale {
		t.Fatalf("want one model-stale alarm, got %+v", al.created)
	}
	if al.created[0].Details["reason"] != StaleReasonSegmentSilent {
		t.Fatalf("want segment-silent reason, got %+v", al.created[0].Details)
	}

	// A sweep with no overdue watermarks is inert (no open, no clear).
	al2 := &fakeAlarms{}
	svc2 := newSvc(&fakeStore{}, al2)
	svc2.sweep(context.Background(), time.Now().UTC())
	if len(al2.created) != 0 {
		t.Fatalf("want no alarm without stale rows, got %+v", al2.created)
	}
}

func TestSweepClaimsFailedAndRaises(t *testing.T) {
	st := &fakeStore{
		failed: []Model{
			{TenantID: "t", GatewayID: "g", ModelID: "m", Version: "1",
				State: StateFailed, Reason: "package-invalid"},
		},
	}
	al := &fakeAlarms{}
	svc := newSvc(st, al)
	svc.sweep(context.Background(), time.Now().UTC())
	if len(al.created) != 1 || al.created[0].AlarmType != AlarmTypeModelFailed {
		t.Fatalf("want one model-failed alarm, got %+v", al.created)
	}
	if al.created[0].Details["reason"] != "package-invalid" {
		t.Fatalf("reason not carried: %+v", al.created[0].Details)
	}
}
