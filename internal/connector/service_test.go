// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// errTestSchemaViolation stands in for connectorcatalog.ErrConfigViolation
// (prepare wraps any validator error under ErrInvalid and tests assert on that).
var errTestSchemaViolation = errors.New("config violates schema")

type fakeAlarms struct {
	created []alarm.CreateInput
	cleared []string
}

func (f *fakeAlarms) CreateAlarm(_ context.Context, in alarm.CreateInput) (alarm.Outcome, error) {
	f.created = append(f.created, in)
	return alarm.Outcome{Created: true, Severity: in.Severity, State: alarm.StateActive}, nil
}
func (f *fakeAlarms) ClearAlarm(_ context.Context, _, _, key string) (alarm.Outcome, error) {
	f.cleared = append(f.cleared, key)
	return alarm.Outcome{}, nil
}

type fakeStore struct {
	gatewayOK        bool
	deviceOK         bool
	templates        map[string]Template
	created          []SaveInput
	updated          []SaveInput
	deleted          []string
	dispatch         []string
	status           []StatusFrame
	tombstones       []DeleteTombstone
	deleteAttempts   []DeleteTombstone
	deleteErrors     []string
	deleteAttemptSeq int64

	pending   []Spec
	newFailed []Spec
	dueFailed []Spec

	// manifest reconciliation
	manifestReported []ReportedInstance
	manifestDesired  []Spec
	bumped           []string

	// watermark drives ReconcileView; nil means the gateway never reported.
	watermark *ManifestWatermark

	lastSpec   Spec
	statusSpec *Spec
}

func (f *fakeStore) GatewayExists(_ context.Context, _, _ string) (bool, error) {
	return f.gatewayOK, nil
}
func (f *fakeStore) DeviceBelongsToGateway(_ context.Context, _, _, _ string) (bool, error) {
	return f.deviceOK, nil
}
func (f *fakeStore) FetchTemplates(_ context.Context, _ string, ids []string) (map[string]Template, error) {
	out := map[string]Template{}
	for _, id := range ids {
		if tpl, ok := f.templates[id]; ok {
			out[id] = tpl
		}
	}
	return out, nil
}
func (f *fakeStore) Create(_ context.Context, _, gateway string, in SaveInput, connectorType, typeVersion string) (Spec, error) {
	f.created = append(f.created, in)
	f.lastSpec = Spec{ID: "s1", TenantID: "t1", GatewayID: gateway,
		ConnectorType: connectorType, TypeVersion: typeVersion,
		Config: in.Config, Mappings: in.Mappings,
		DeviceID: in.DeviceID, ProfileID: in.ProfileID,
		Revision: 1, State: StatePending}
	return f.lastSpec, nil
}
func (f *fakeStore) Update(_ context.Context, _, specID string, in SaveInput, connectorType, typeVersion string) (Spec, error) {
	f.updated = append(f.updated, in)
	f.lastSpec = Spec{ID: specID, TenantID: "t1", GatewayID: "g1",
		ConnectorType: connectorType, TypeVersion: typeVersion,
		Config: in.Config, Mappings: in.Mappings,
		DeviceID: in.DeviceID, ProfileID: in.ProfileID,
		Revision: 2, State: StatePending}
	return f.lastSpec, nil
}
func (f *fakeStore) Delete(_ context.Context, _, specID string) (Spec, error) {
	f.deleted = append(f.deleted, specID)
	sp := Spec{ID: specID, TenantID: "t1", GatewayID: "g1",
		ConnectorType: "edge-manager-resource", TypeVersion: "1", Revision: 3, State: StateApplied}
	f.tombstones = append(f.tombstones, DeleteTombstone{
		TenantID: sp.TenantID, GatewayID: sp.GatewayID, SpecID: sp.ID,
		ConnectorType: sp.ConnectorType, TypeVersion: sp.TypeVersion, DeleteRevision: sp.Revision,
	})
	return sp, nil
}
func (f *fakeStore) Get(_ context.Context, _, specID string) (Spec, error) {
	if f.statusSpec != nil {
		return *f.statusSpec, nil
	}
	return Spec{ID: specID, TenantID: "t1", GatewayID: "g1",
		ConnectorType: "modbus-tcp", Revision: 3, State: StateApplied}, nil
}
func (f *fakeStore) ListByGateway(_ context.Context, _, _ string) ([]Spec, error) {
	return []Spec{f.lastSpec}, nil
}
func (f *fakeStore) ListAllForGateway(_ context.Context, _ string) ([]Spec, error) {
	return []Spec{{ID: "s1", TenantID: "t1", GatewayID: "g1",
		ConnectorType: "modbus-tcp", Revision: 1, State: StateApplied}}, nil
}
func (f *fakeStore) ListDeleteTombstones(_ context.Context, _ string, limit int) ([]DeleteTombstone, error) {
	out := append([]DeleteTombstone(nil), f.tombstones...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].LastAttemptAt == nil) != (out[j].LastAttemptAt == nil) {
			return out[i].LastAttemptAt == nil
		}
		if out[i].LastAttemptAt != nil && out[j].LastAttemptAt != nil &&
			!out[i].LastAttemptAt.Equal(*out[j].LastAttemptAt) {
			return out[i].LastAttemptAt.Before(*out[j].LastAttemptAt)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeStore) MarkDeleteAttempt(_ context.Context, tombstone DeleteTombstone, at time.Time, lastError string) error {
	f.deleteAttempts = append(f.deleteAttempts, tombstone)
	f.deleteErrors = append(f.deleteErrors, lastError)
	f.deleteAttemptSeq++
	attemptAt := at.Add(time.Duration(f.deleteAttemptSeq))
	for i := range f.tombstones {
		if f.tombstones[i].TenantID == tombstone.TenantID &&
			f.tombstones[i].GatewayID == tombstone.GatewayID &&
			f.tombstones[i].SpecID == tombstone.SpecID &&
			f.tombstones[i].DeleteRevision == tombstone.DeleteRevision {
			f.tombstones[i].LastAttemptAt = &attemptAt
		}
	}
	return nil
}
func (f *fakeStore) MarkDispatched(_ context.Context, _, specID string, _ time.Time) error {
	f.dispatch = append(f.dispatch, specID)
	return nil
}
func (f *fakeStore) ApplyStatus(_ context.Context, _ string, frame StatusFrame, _ time.Time) (Spec, error) {
	f.status = append(f.status, frame)
	if f.statusSpec != nil {
		sp := *f.statusSpec
		if sp.Revision == frame.Revision {
			sp.State = frame.State
			sp.ReportedRevision = frame.Revision
			sp.ReportReason = frame.Reason
			sp.Stats = frame.Stats
			sp.ObservedRevision = frame.ObservedRevision
			sp.ObservedHash = frame.ObservedHash
		}
		return sp, nil
	}
	return Spec{ID: frame.SpecID, TenantID: "t1", GatewayID: "g1",
		ConnectorType: "modbus-tcp", Revision: 1, State: frame.State,
		ReportReason: frame.Reason, Stats: frame.Stats,
		ObservedRevision: frame.ObservedRevision, ObservedHash: frame.ObservedHash}, nil
}
func (f *fakeStore) ClaimPendingForAlarm(_ context.Context, _, _ time.Time) ([]Spec, error) {
	return f.pending, nil
}
func (f *fakeStore) ClaimFailedForRaise(_ context.Context, _ time.Time) ([]Spec, error) {
	return f.newFailed, nil
}
func (f *fakeStore) ClaimFailedForEscalation(_ context.Context, _, _ time.Time) ([]Spec, error) {
	return f.dueFailed, nil
}

func (f *fakeStore) StampManifest(_ context.Context, _, _ string, reported []ReportedInstance, _ time.Time) ([]Spec, error) {
	f.manifestReported = reported
	return f.manifestDesired, nil
}

func (f *fakeStore) GetWatermark(_ context.Context, _, _ string) (ManifestWatermark, error) {
	if f.watermark == nil {
		return ManifestWatermark{}, ErrNotFound
	}
	return *f.watermark, nil
}

func (f *fakeStore) BumpRevision(_ context.Context, _, specID string) (Spec, error) {
	f.bumped = append(f.bumped, specID)
	for i := range f.manifestDesired {
		if f.manifestDesired[i].ID == specID {
			f.manifestDesired[i].Revision++
			return f.manifestDesired[i], nil
		}
	}
	return Spec{ID: specID, TenantID: "t1", GatewayID: "g1",
		ConnectorType: "modbus-tcp", Revision: 2}, nil
}

func testSvc(st Store, al AlarmRaiser) *Service {
	return NewService(nil, st, al, DefaultTuning(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func input() SaveInput {
	return SaveInput{ConnectorType: "modbus-tcp", TypeVersion: "1.0.0"}
}

func TestCreateRejectsUnknownGateway(t *testing.T) {
	st := &fakeStore{gatewayOK: false}
	s := testSvc(st, &fakeAlarms{})
	if _, err := s.CreateSpec(context.Background(), "t1", "g1", input()); err != ErrGatewayNotFound {
		t.Fatalf("want ErrGatewayNotFound, got %v", err)
	}
	if len(st.created) != 0 {
		t.Fatal("must not persist for an unknown/non-gateway target")
	}
}

func TestCreatePersistsAndDispatches(t *testing.T) {
	st := &fakeStore{gatewayOK: true}
	s := testSvc(st, &fakeAlarms{})
	sp, err := s.CreateSpec(context.Background(), "t1", "g1", input())
	if err != nil {
		t.Fatal(err)
	}
	if sp.Revision != 1 || len(st.created) != 1 || len(st.dispatch) != 1 || st.dispatch[0] != "s1" {
		t.Fatalf("unexpected create side effects: %+v dispatch=%v", st.created, st.dispatch)
	}
}

func TestUpdateBumpsAndDispatches(t *testing.T) {
	st := &fakeStore{gatewayOK: true}
	s := testSvc(st, &fakeAlarms{})
	if _, err := s.UpdateSpec(context.Background(), "t1", "s1", input()); err != nil {
		t.Fatal(err)
	}
	if len(st.updated) != 1 || len(st.dispatch) != 1 {
		t.Fatalf("expected one update and one dispatch, got %+v %v", st.updated, st.dispatch)
	}
}

func TestDeletePublishesRemovalAndDeletes(t *testing.T) {
	st := &fakeStore{}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	if err := s.DeleteSpec(context.Background(), "t1", "s1"); err != nil {
		t.Fatal(err)
	}
	if len(st.deleted) != 1 || st.deleted[0] != "s1" {
		t.Fatalf("expected row delete, got %v", st.deleted)
	}
	// Every escalation alarm for the removed spec must be cleared.
	if len(al.cleared) != 3 {
		t.Fatalf("expected pending+failed+degraded alarm clear, got %v", al.cleared)
	}
}

func TestDeleteCommitsBeforePublishAndReplaysAfterFailure(t *testing.T) {
	st := &fakeStore{}
	s := testSvc(st, &fakeAlarms{})
	var frames []PushFrame
	s.publishFunc = func(_ context.Context, frame PushFrame) error {
		if len(st.deleted) != 1 || len(st.tombstones) != 1 {
			t.Fatal("delete frame published before desired+tombstone commit")
		}
		frames = append(frames, frame)
		if len(frames) == 1 {
			return errors.New("NATS unavailable")
		}
		return nil
	}
	if err := s.DeleteSpec(context.Background(), "t1", "s1"); err != nil {
		t.Fatalf("durable delete must survive publish failure: %v", err)
	}
	if len(frames) != 1 || frames[0].Kind != KindDelete || frames[0].SpecID != "s1" ||
		frames[0].Revision != 3 || len(st.tombstones) != 1 {
		t.Fatalf("unexpected delete persistence/frame: frames=%+v tombstones=%+v", frames, st.tombstones)
	}
	if len(st.deleteErrors) != 1 || st.deleteErrors[0] == "" {
		t.Fatalf("publish failure was not persisted on the attempt: %v", st.deleteErrors)
	}

	s.Redrive(context.Background(), "t1", "g1")
	if len(frames) < 2 || frames[1].Kind != KindDelete || frames[1].Revision != 3 {
		t.Fatalf("post-hello redrive did not replay the exact delete: %+v", frames)
	}
	if len(st.tombstones) != 1 {
		t.Fatal("transport redrive must not retire an unacknowledged tombstone")
	}
}

func TestDeleteReplayIsBoundedAndFair(t *testing.T) {
	st := &fakeStore{}
	base := time.Now().UTC()
	for i := 0; i < deleteReplayBatchSize+1; i++ {
		st.tombstones = append(st.tombstones, DeleteTombstone{
			TenantID: "t1", GatewayID: "g1",
			SpecID: fmt.Sprintf("spec-%03d", i), DeleteRevision: 1,
			CreatedAt: base.Add(time.Duration(i)),
		})
	}
	s := testSvc(st, &fakeAlarms{})
	firstBatch := map[string]bool{}
	s.publishFunc = func(_ context.Context, frame PushFrame) error {
		if frame.Kind == KindDelete {
			firstBatch[frame.SpecID] = true
		}
		return nil
	}

	s.Redrive(context.Background(), "t1", "g1")
	if len(firstBatch) != deleteReplayBatchSize {
		t.Fatalf("first replay batch = %d, want %d", len(firstBatch), deleteReplayBatchSize)
	}
	if !firstBatch["spec-000"] || !firstBatch[fmt.Sprintf("spec-%03d", deleteReplayBatchSize-1)] ||
		firstBatch[fmt.Sprintf("spec-%03d", deleteReplayBatchSize)] {
		t.Fatalf("first batch did not select the oldest bounded set: %d items", len(firstBatch))
	}

	secondBatch := map[string]bool{}
	s.publishFunc = func(_ context.Context, frame PushFrame) error {
		if frame.Kind == KindDelete {
			secondBatch[frame.SpecID] = true
		}
		return nil
	}
	s.Redrive(context.Background(), "t1", "g1")
	if len(secondBatch) != deleteReplayBatchSize {
		t.Fatalf("second replay batch = %d, want %d", len(secondBatch), deleteReplayBatchSize)
	}
	if !secondBatch[fmt.Sprintf("spec-%03d", deleteReplayBatchSize)] {
		t.Fatal("never-attempted newest tombstone was starved by retained older tombstones")
	}
	if secondBatch[fmt.Sprintf("spec-%03d", deleteReplayBatchSize-1)] {
		t.Fatal("most recently attempted tombstone was selected before the full backlog rotated")
	}
}

func TestStatusRevisionFenceHasNoAlarmSideEffects(t *testing.T) {
	current := Spec{ID: "s1", TenantID: "t1", GatewayID: "g1", Revision: 2, State: StatePending}
	st := &fakeStore{statusSpec: &current}
	alarms := &fakeAlarms{}
	s := testSvc(st, alarms)
	for _, revision := range []int64{1, 3} {
		sp, err := s.HandleStatus(context.Background(), "t1", StatusFrame{
			SpecID: "s1", State: StateApplied, Revision: revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if sp.Revision != 2 || sp.State != StatePending {
			t.Fatalf("fenced status mutated current state: %+v", sp)
		}
	}
	if len(alarms.cleared) != 0 || len(alarms.created) != 0 {
		t.Fatalf("stale/future status caused alarm side effects: %+v", alarms)
	}
}

func TestJobMetricsPreservesZeroAndFieldAvailability(t *testing.T) {
	zero := uint64(0)
	latency := 12.5
	current := Spec{ID: "s1", TenantID: "t1", GatewayID: "g1", Stats: &StatusStats{
		SuccessTotal: &zero, P50MS: &latency,
	}}
	s := testSvc(&fakeStore{statusSpec: &current}, nil)
	metrics, available, err := s.JobMetrics(context.Background(), "t1", "s1")
	if err != nil || !available {
		t.Fatalf("zero-valued stats must be available: available=%v err=%v", available, err)
	}
	if metrics.SuccessTotal == nil || *metrics.SuccessTotal != 0 || metrics.ErrorTotal != nil ||
		metrics.P50MS == nil || *metrics.P50MS != latency || metrics.P95MS != nil ||
		metrics.EventsPerSecond != nil {
		t.Fatalf("optional metrics were fabricated or merged: %+v", metrics)
	}
	current.Stats = &StatusStats{}
	if _, available, err := s.JobMetrics(context.Background(), "t1", "s1"); err != nil || available {
		t.Fatalf("empty stats must be unavailable: available=%v err=%v", available, err)
	}
	current.Stats = nil
	if _, available, err := s.JobMetrics(context.Background(), "t1", "s1"); err != nil || available {
		t.Fatalf("absent stats must be unavailable: available=%v err=%v", available, err)
	}
}

func TestAppliedStatusClearsAlarms(t *testing.T) {
	st := &fakeStore{}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	if _, err := s.HandleStatus(context.Background(), "t1",
		StatusFrame{SpecID: "s1", State: StateApplied, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if len(al.cleared) != 3 {
		t.Fatalf("APPLIED must clear pending/failed/degraded, got %v", al.cleared)
	}
	if len(al.created) != 0 {
		t.Fatal("APPLIED must not raise")
	}
}

func TestDegradedStatusRaisesWarningAndClearsOthers(t *testing.T) {
	st := &fakeStore{}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	sp, err := s.HandleStatus(context.Background(), "t1",
		StatusFrame{SpecID: "s1", State: StateDegraded, Revision: 1, Reason: "idle-no-inference"})
	if err != nil {
		t.Fatal(err)
	}
	if len(al.created) != 1 || al.created[0].AlarmType != AlarmTypeDegraded ||
		al.created[0].Severity != alarm.SeverityWarning {
		t.Fatalf("want one generic DEGRADED WARNING, got %+v", al.created)
	}
	if al.created[0].Details["reason"] != "idle-no-inference" {
		t.Fatalf("reason not carried verbatim: %+v", al.created[0].Details)
	}
	// Pending and failed are cleared; degraded is the live state.
	wantCleared := map[string]bool{DedupePendingKey(sp.ID): false, DedupeFailedKey(sp.ID): false}
	for _, k := range al.cleared {
		if _, ok := wantCleared[k]; ok {
			wantCleared[k] = true
		}
	}
	for k, got := range wantCleared {
		if !got {
			t.Errorf("expected clear %q, got %v", k, al.cleared)
		}
	}
}

func TestFailedStatusDoesNotRaiseInline(t *testing.T) {
	st := &fakeStore{}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	if _, err := s.HandleStatus(context.Background(), "t1",
		StatusFrame{SpecID: "s1", State: StateFailed, Reason: "connect refused"}); err != nil {
		t.Fatal(err)
	}
	// The MAJOR alarm is raised durably by the sweep claim, not inline.
	if len(al.created) != 0 {
		t.Fatalf("FAILED must defer raising to the sweep, got %+v", al.created)
	}
}

func TestStatusRequiresSpecID(t *testing.T) {
	s := testSvc(&fakeStore{}, &fakeAlarms{})
	if _, err := s.HandleStatus(context.Background(), "t1",
		StatusFrame{State: StateApplied}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for missing spec_id, got %v", err)
	}
}

func TestSweepPendingWarningFailedMajorThenCritical(t *testing.T) {
	st := &fakeStore{
		pending:   []Spec{{ID: "p1", TenantID: "t1", GatewayID: "g1", Revision: 1}},
		newFailed: []Spec{{ID: "f1", TenantID: "t1", GatewayID: "g1", Revision: 1}},
	}
	al := &fakeAlarms{}
	s := testSvc(st, al)
	s.sweep(context.Background(), time.Now().UTC())
	if len(al.created) != 2 {
		t.Fatalf("want pending WARNING + failed MAJOR, got %d", len(al.created))
	}
	if al.created[0].Severity != alarm.SeverityWarning ||
		al.created[0].AlarmType != AlarmTypePending ||
		al.created[0].DedupeKey != DedupePendingKey("p1") {
		t.Fatalf("bad pending alarm: %+v", al.created[0])
	}
	if al.created[1].Severity != alarm.SeverityMajor ||
		al.created[1].AlarmType != AlarmTypeFailed {
		t.Fatalf("bad failed alarm: %+v", al.created[1])
	}

	// Second sweep with a due escalation bumps the same failed alarm.
	st2 := &fakeStore{dueFailed: []Spec{{ID: "f1", TenantID: "t1", GatewayID: "g1",
		ReportReason: "still down"}}}
	al2 := &fakeAlarms{}
	s2 := testSvc(st2, al2)
	s2.sweep(context.Background(), time.Now().UTC())
	if len(al2.created) != 1 || al2.created[0].Severity != alarm.SeverityCritical {
		t.Fatalf("want one CRITICAL escalation, got %+v", al2.created)
	}
}

func TestRedrivePushesFullSet(t *testing.T) {
	st := &fakeStore{}
	s := testSvc(st, &fakeAlarms{})
	s.Redrive(context.Background(), "t1", "g1")
	if len(st.dispatch) != 1 || st.dispatch[0] != "s1" {
		t.Fatalf("expected the full desired set re-dispatched, got %v", st.dispatch)
	}
}

func TestDedupeKeysPerSpec(t *testing.T) {
	if DedupePendingKey("a") == DedupePendingKey("b") {
		t.Fatal("pending dedupe keys must differ per spec")
	}
	if DedupeFailedKey("a") == DedupePendingKey("a") {
		t.Fatal("pending and failed alarms must be distinct dedupe keys")
	}
}

func TestCreateRejectsDeviceOutsideGateway(t *testing.T) {
	st := &fakeStore{gatewayOK: true, deviceOK: false}
	s := testSvc(st, &fakeAlarms{})
	_, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ConnectorType: "modbus-tcp", DeviceID: "d1",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for a foreign sub-device, got %v", err)
	}
	if len(st.created) != 0 {
		t.Fatal("foreign sub-device must not be persisted")
	}
}

func TestCreateResolvesTypeFromProfileTemplate(t *testing.T) {
	st := &fakeStore{gatewayOK: true, templates: map[string]Template{
		"p1": {ProfileID: "p1", ConnectorType: "modbus-tcp", TypeVersion: "1.0",
			Config:   json.RawMessage(`{"host":"tpl","port":502}`),
			Mappings: json.RawMessage(`[{"register":"40001"}]`)},
	}}
	s := testSvc(st, &fakeAlarms{})
	sp, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ProfileID: "p1", Config: json.RawMessage(`{"host":"dev"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Persisted connector_type comes from the template; raw override config
	// is stored, not the merged result.
	if sp.ConnectorType != "modbus-tcp" {
		t.Fatalf("type must resolve from template, got %q", sp.ConnectorType)
	}
	// The dispatched frame carries the merged effective spec.
	frame, err := s.frameFor(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if json.Unmarshal(frame.Config, &m) != nil {
		t.Fatalf("merged config invalid: %s", frame.Config)
	}
	if m["host"] != "dev" || m["port"].(float64) != 502 {
		t.Fatalf("override host must win, template port preserved: %v", m)
	}
	if string(frame.Mappings) != `[{"register":"40001"}]` {
		t.Fatalf("template mappings must be inherited, got %s", frame.Mappings)
	}
}

func TestCreateRejectsUnknownOrTemplatelessProfile(t *testing.T) {
	s := testSvc(&fakeStore{gatewayOK: true}, &fakeAlarms{})
	if _, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ProfileID: "missing",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown profile must be ErrInvalid, got %v", err)
	}

	noTemplate := &fakeStore{gatewayOK: true, templates: map[string]Template{
		"p2": {ProfileID: "p2", ConnectorType: ""},
	}}
	s2 := testSvc(noTemplate, &fakeAlarms{})
	if _, err := s2.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ProfileID: "p2",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("profile without template must be ErrInvalid, got %v", err)
	}
}

func TestCreateRequiresTypeOrProfile(t *testing.T) {
	s := testSvc(&fakeStore{gatewayOK: true}, &fakeAlarms{})
	if _, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("type-less, profile-less create must be ErrInvalid, got %v", err)
	}
}

type fakeValidator struct {
	err             error
	calls           int
	configCalls     int
	overrideCalls   int
	lastType        string
	overrideErr     error
	resolvedVersion string
	resolveCalls    int
}

func (f *fakeValidator) ResolveVersion(_ context.Context, _, _, version string) (string, error) {
	f.resolveCalls++
	if version != "" {
		return version, nil
	}
	return f.resolvedVersion, nil
}

func (f *fakeValidator) ValidateConfig(_ context.Context, _, connectorType, _ string, _ json.RawMessage) error {
	f.calls++
	f.configCalls++
	f.lastType = connectorType
	return f.err
}

func (f *fakeValidator) ValidateOverride(_ context.Context, _, connectorType, _ string, _ json.RawMessage) error {
	f.calls++
	f.overrideCalls++
	f.lastType = connectorType
	if f.overrideErr != nil {
		return f.overrideErr
	}
	return f.err
}

func TestCreateValidatesConfigAgainstSchema(t *testing.T) {
	// A violating instance override is rejected before persistence.
	st := &fakeStore{gatewayOK: true}
	s := testSvc(st, &fakeAlarms{})
	violation := fmt.Errorf("%w: missing host", errTestSchemaViolation)
	s.SetConfigValidator(&fakeValidator{err: violation})
	_, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ConnectorType: "modbus-tcp",
		Config:        json.RawMessage(`{"port":502}`),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("schema-violating config must map to ErrInvalid, got %v", err)
	}
	if len(st.created) != 0 {
		t.Fatal("violating config must not be persisted")
	}

	// A valid config passes and is persisted once; empty config is not
	// separately validated (the template/normalized {} stands).
	st2 := &fakeStore{gatewayOK: true}
	s2 := testSvc(st2, &fakeAlarms{})
	v := &fakeValidator{}
	s2.SetConfigValidator(v)
	if _, err := s2.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ConnectorType: "modbus-tcp",
		Config:        json.RawMessage(`{"host":"10.0.0.1"}`),
	}); err != nil {
		t.Fatalf("valid config must pass, got %v", err)
	}
	if v.calls != 1 || v.lastType != "modbus-tcp" {
		t.Fatalf("validator must be invoked once for the type, calls=%d", v.calls)
	}
}

func TestCreateValidatesTemplateAndOverride(t *testing.T) {
	st := &fakeStore{gatewayOK: true, templates: map[string]Template{
		"p1": {ProfileID: "p1", ConnectorType: "modbus-tcp", TypeVersion: "1.0",
			Config: json.RawMessage(`{"host":"tpl"}`)},
	}}
	s := testSvc(st, &fakeAlarms{})
	v := &fakeValidator{}
	s.SetConfigValidator(v)
	if _, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ProfileID: "p1",
		Config:    json.RawMessage(`{"port":502}`),
	}); err != nil {
		t.Fatalf("valid template+override must pass, got %v", err)
	}
	// Template and override are validated independently -> 2 calls.
	if v.calls != 2 {
		t.Fatalf("want template+override validated (2 calls), got %d", v.calls)
	}
}

func TestCreateResolvesBlankTypeVersion(t *testing.T) {
	// A blank type_version is resolved by NL to the highest registered
	// version and that resolved value is what gets persisted.
	st := &fakeStore{gatewayOK: true}
	s := testSvc(st, &fakeAlarms{})
	v := &fakeValidator{resolvedVersion: "2.1"}
	s.SetConfigValidator(v)
	sp, err := s.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ConnectorType: "modbus-tcp",
		Config:        json.RawMessage(`{"host":"10.0.0.1"}`),
	})
	if err != nil {
		t.Fatalf("create must pass, got %v", err)
	}
	if sp.TypeVersion != "2.1" {
		t.Fatalf("want resolved version 2.1 persisted, got %q", sp.TypeVersion)
	}
	if st.lastSpec.TypeVersion != "2.1" {
		t.Fatalf("store must receive resolved version, got %q", st.lastSpec.TypeVersion)
	}

	// An explicit version is passed through untouched and never resolved.
	st2 := &fakeStore{gatewayOK: true}
	s2 := testSvc(st2, &fakeAlarms{})
	v2 := &fakeValidator{resolvedVersion: "9.9"}
	s2.SetConfigValidator(v2)
	sp2, err := s2.CreateSpec(context.Background(), "t1", "g1", SaveInput{
		ConnectorType: "modbus-tcp", TypeVersion: "1.0",
		Config: json.RawMessage(`{"host":"x"}`),
	})
	if err != nil {
		t.Fatalf("create must pass, got %v", err)
	}
	if sp2.TypeVersion != "1.0" || v2.resolveCalls != 0 {
		t.Fatalf("explicit version must be preserved without resolving, got %q calls=%d",
			sp2.TypeVersion, v2.resolveCalls)
	}
}

func TestIsOpen(t *testing.T) {
	for _, state := range []string{StatePending, StateDispatched, StateCreating} {
		if !IsOpen(state) {
			t.Errorf("%s must be open", state)
		}
	}
	for _, state := range []string{StateApplied, StateFailed, StateDegraded} {
		if IsOpen(state) {
			t.Errorf("%s must not be open", state)
		}
	}
}
