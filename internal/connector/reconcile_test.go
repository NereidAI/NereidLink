// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// reconcile_test.go covers the desired/reported partition and the
// observe-vs-strict behavior of connector manifest reconciliation.
package connector

import (
	"context"
	"encoding/json"
	"testing"
)

func recSpec(id string, rev int64, config, mappings json.RawMessage) Spec {
	return Spec{ID: id, TenantID: "t1", GatewayID: "g1",
		ConnectorType: "modbus-tcp", Revision: rev,
		Config: config, Mappings: mappings, ProfileID: ""}
}

func hashOf(id string, sp Spec) string {
	return canonicalHash(sp.Config, sp.Mappings)
}

func TestReconcileClassificationObserve(t *testing.T) {
	sync := recSpec("sync", 1, json.RawMessage(`{"host":"a"}`), json.RawMessage(`[]`))
	missing := recSpec("missing", 1, json.RawMessage(`{"host":"b"}`), json.RawMessage(`[]`))
	stale := recSpec("stale", 3, json.RawMessage(`{"host":"c"}`), json.RawMessage(`[]`))
	drifted := recSpec("drifted", 2, json.RawMessage(`{"host":"d"}`), json.RawMessage(`[]`))

	st := &fakeStore{gatewayOK: true, manifestDesired: []Spec{sync, missing, stale, drifted}}
	al := &fakeAlarms{}
	s := testSvc(st, al) // StrictReconcile defaults to false

	rep, err := s.ReconcileReported(context.Background(), "t1", "g1", []ReportedInstance{
		{SpecID: "sync", Revision: 1, ConfigHash: hashOf("sync", sync)},
		{SpecID: "stale", Revision: 2},
		{SpecID: "drifted", Revision: 2, ConfigHash: "deadbeef"},
		{SpecID: "extra-1", Revision: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.InSync) != 1 || rep.InSync[0] != "sync" {
		t.Fatalf("in_sync wrong: %v", rep.InSync)
	}
	if len(rep.Missing) != 1 || rep.Missing[0] != "missing" {
		t.Fatalf("missing wrong: %v", rep.Missing)
	}
	if len(rep.Stale) != 1 || rep.Stale[0] != "stale" {
		t.Fatalf("stale wrong: %v", rep.Stale)
	}
	if len(rep.Drifted) != 1 || rep.Drifted[0] != "drifted" {
		t.Fatalf("drifted wrong: %v", rep.Drifted)
	}
	if len(rep.Extra) != 1 || rep.Extra[0] != "extra-1" {
		t.Fatalf("extra wrong: %v", rep.Extra)
	}
	// Observe mode opens one summarizing drift alarm and never mutates/pushes.
	if len(al.created) != 1 || al.created[0].AlarmType != AlarmTypeDrift {
		t.Fatalf("want one DRIFT alarm, got %+v", al.created)
	}
	if len(st.bumped) != 0 || len(st.dispatch) != 0 {
		t.Fatalf("observe mode must not bump/dispatch: bump=%v dispatch=%v", st.bumped, st.dispatch)
	}
}

func TestReconcileInSyncClearsDriftAlarm(t *testing.T) {
	sp := recSpec("s1", 1, json.RawMessage(`{"host":"a"}`), json.RawMessage(`[]`))
	st := &fakeStore{manifestDesired: []Spec{sp}}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	rep, err := s.ReconcileReported(context.Background(), "t1", "g1", []ReportedInstance{
		{SpecID: "s1", Revision: 1, ConfigHash: hashOf("s1", sp)},
	})
	if err != nil || rep.Drift() {
		t.Fatalf("want converged, drift=%v err=%v", rep.Drift(), err)
	}
	if len(al.created) != 0 {
		t.Fatal("in-sync must not raise a drift alarm")
	}
	if len(al.cleared) != 1 || al.cleared[0] != DedupeDriftKey("g1") {
		t.Fatalf("want drift alarm clear by gateway key, got %v", al.cleared)
	}
}

func TestStrictReconcileEnforcesDesired(t *testing.T) {
	missing := recSpec("missing", 1, json.RawMessage(`{"host":"m"}`), json.RawMessage(`[]`))
	stale := recSpec("stale", 2, json.RawMessage(`{"host":"s"}`), json.RawMessage(`[]`))
	drifted := recSpec("drifted", 1, json.RawMessage(`{"host":"d"}`), json.RawMessage(`[]`))
	sync := recSpec("sync", 1, json.RawMessage(`{"host":"x"}`), json.RawMessage(`[]`))

	st := &fakeStore{gatewayOK: true,
		manifestDesired: []Spec{missing, stale, drifted, sync}}
	s := testSvc(st, &fakeAlarms{})
	tune := DefaultTuning()
	tune.StrictReconcile = true
	s.tune = tune
	var frames []PushFrame
	s.publishFunc = func(_ context.Context, frame PushFrame) error {
		frames = append(frames, frame)
		return nil
	}

	_, err := s.ReconcileReported(context.Background(), "t1", "g1", []ReportedInstance{
		{SpecID: "stale", Revision: 1},
		{SpecID: "drifted", Revision: 1, ConfigHash: "mutated"},
		{SpecID: "sync", Revision: 1, ConfigHash: hashOf("sync", sync)},
		{SpecID: "rogue", Revision: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Missing + stale + drifted each trigger a dispatch (MarkDispatched).
	wantDispatch := map[string]bool{"missing": true, "stale": true, "drifted": true}
	got := map[string]bool{}
	for _, id := range st.dispatch {
		got[id] = true
	}
	if len(got) != 3 || !got["missing"] || !got["stale"] || !got["drifted"] {
		t.Fatalf("strict must re-push missing/stale/drifted, got %v", st.dispatch)
	}
	// Only the same-revision content drift needs a revision bump.
	if len(st.bumped) != 1 || st.bumped[0] != "drifted" {
		t.Fatalf("only drifted must be bumped, got %v", st.bumped)
	}
	for _, frame := range frames {
		if frame.Kind == KindDelete {
			t.Fatalf("type-less manifest Extra must not auto-delete manager/filter/legacy resources: %+v", frame)
		}
	}
	if len(st.tombstones) != 0 {
		t.Fatalf("manifest Extra created an implicit delete tombstone: %+v", st.tombstones)
	}
	_ = wantDispatch
}

func TestNoHashSkipsContentDrift(t *testing.T) {
	sp := recSpec("s1", 1, json.RawMessage(`{"host":"a"}`), json.RawMessage(`[]`))
	st := &fakeStore{manifestDesired: []Spec{sp}}
	s := testSvc(st, &fakeAlarms{})
	// Same revision but no hash reported -> cannot assess content, in_sync.
	rep, err := s.ReconcileReported(context.Background(), "t1", "g1",
		[]ReportedInstance{{SpecID: "s1", Revision: 1}})
	if err != nil || len(rep.InSync) != 1 || rep.Drift() {
		t.Fatalf("missing hash must not imply drift: %+v err=%v", rep, err)
	}
}

func TestAbsentConnectorSegmentNotCalled(t *testing.T) {
	// The ingress guards a nil frame.Connectors; a nil reported slice with a
	// present-but-empty array means every desired instance is missing.
	sp := recSpec("s1", 1, json.RawMessage(`{"host":"a"}`), json.RawMessage(`[]`))
	st := &fakeStore{manifestDesired: []Spec{sp}}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	rep, err := s.ReconcileReported(context.Background(), "t1", "g1",
		[]ReportedInstance{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missing) != 1 || !rep.Drift() {
		t.Fatalf("explicit empty segment must classify desired as missing: %+v", rep)
	}
}

func TestCanonicalHashDeterministic(t *testing.T) {
	cfg := json.RawMessage(`{"b":2,"a":{"z":1,"y":2}}`)
	maps := json.RawMessage(`[{"k":1}]`)
	h1 := canonicalHash(cfg, maps)
	h2 := canonicalHash(
		json.RawMessage(`{ "a": {"y":2,"z":1}, "b":2 }`),
		json.RawMessage(`[{"k":1}]`))
	if h1 == "" || h1 != h2 {
		t.Fatalf("hash must be key-order/whitespace independent: %q vs %q", h1, h2)
	}
	if canonicalHash(json.RawMessage(`{"b":3}`), maps) == h1 {
		t.Fatal("different content must hash differently")
	}
}
