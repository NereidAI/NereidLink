// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package linknotify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

type fakeAlarms struct {
	created []alarm.CreateInput
	cleared []string
}

func (f *fakeAlarms) CreateAlarm(_ context.Context, in alarm.CreateInput) (alarm.Outcome, error) {
	f.created = append(f.created, in)
	return alarm.Outcome{AlarmID: "al-" + lenStr(f.created), Created: true, Severity: in.Severity, State: alarm.StateActive}, nil
}
func (f *fakeAlarms) ClearAlarm(_ context.Context, _, _, _ string) (alarm.Outcome, error) {
	f.cleared = append(f.cleared, "x")
	return alarm.Outcome{}, nil
}

func lenStr(n []alarm.CreateInput) string {
	return string(rune('0' + len(n)))
}

type fakeStore struct {
	open    []Episode
	closed  []string
	claim   []Episode
	digRows []DigestRow
	marked  []string
	emails  []string
	allow   bool
}

func (f *fakeStore) OpenEpisode(_ context.Context, ep Episode) error {
	f.open = append(f.open, ep)
	return nil
}
func (f *fakeStore) CloseEpisode(_ context.Context, gw string, _ time.Time) error {
	f.closed = append(f.closed, gw)
	return nil
}
func (f *fakeStore) ClaimDueEpisodes(_ context.Context, _, _ time.Time) ([]Episode, error) {
	return f.claim, nil
}
func (f *fakeStore) TenantsWithEndedEpisodes(_ context.Context, _ time.Time) ([]string, error) {
	return []string{"t1"}, nil
}
func (f *fakeStore) DigestRows(_ context.Context, _ string, _ time.Time) ([]DigestRow, error) {
	return f.digRows, nil
}
func (f *fakeStore) MarkDigestSent(_ context.Context, _ string, _, _ time.Time) (bool, error) {
	f.marked = append(f.marked, "x")
	return f.allow, nil
}
func (f *fakeStore) AdminEmails(_ context.Context, _ string) ([]string, error) {
	return f.emails, nil
}

type fakeSink struct {
	inbox []string
	sent  int
}

func (s *fakeSink) RecordInbox(_ context.Context, _, _, _, title, _ string) error {
	s.inbox = append(s.inbox, title)
	return nil
}
func (s *fakeSink) Deliver(_ context.Context, _, _, _ string, _ []string) error {
	s.sent++
	return nil
}

func newSvc(store EpisodeStore, alarms AlarmRaiser, sink DigestSink) *Service {
	return NewService(nil, store, alarms, sink,
		Tuning{EscalationAfter: 30 * time.Minute, DigestEnabled: true, DigestHourUTC: 8},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func advJSON(kind, reason string) []byte {
	raw, _ := json.Marshal(advisory{
		Kind: kind, Time: time.Unix(1000, 0).UTC(), TenantID: "t1",
		GatewayID: "g1", Reason: reason,
	})
	return raw
}

func TestDownOpensMajorAlarmAndEpisode(t *testing.T) {
	store, alarms, sink := &fakeStore{}, &fakeAlarms{}, &fakeSink{}
	s := newSvc(store, alarms, sink)

	s.handle(context.Background(), advJSON(kindDown, "lease_expired"))

	if len(alarms.created) != 1 {
		t.Fatalf("expected one alarm, got %d", len(alarms.created))
	}
	in := alarms.created[0]
	if in.Severity != InitialSeverity || in.AlarmType != AlarmType ||
		in.DedupeKey != dedupeKey("g1") {
		t.Fatalf("unexpected alarm input: %+v", in)
	}
	if len(store.open) != 1 || store.open[0].GatewayID != "g1" || store.open[0].AlarmID == "" {
		t.Fatalf("expected one episode linked to alarm, got %+v", store.open)
	}
}

func TestFlappingAlsoOpens(t *testing.T) {
	store, alarms, sink := &fakeStore{}, &fakeAlarms{}, &fakeSink{}
	s := newSvc(store, alarms, sink)
	s.handle(context.Background(), advJSON(kindFlapping, "transport_down"))
	if len(alarms.created) != 1 {
		t.Fatalf("flapping down must open an alarm, got %d", len(alarms.created))
	}
	if v, _ := alarms.created[0].Details["flapping"].(bool); !v {
		t.Fatal("flapping detail must be marked")
	}
}

func TestPlannedAndSuspectDoNotAlarm(t *testing.T) {
	store, alarms, sink := &fakeStore{}, &fakeAlarms{}, &fakeSink{}
	s := newSvc(store, alarms, sink)
	s.handle(context.Background(), advJSON(kindPlanned, "planned_down"))
	s.handle(context.Background(), advJSON(kindSuspect, "missed_beat"))
	if len(alarms.created) != 0 || len(store.open) != 0 {
		t.Fatalf("planned/suspect must not alarm, got alarms=%d episodes=%d",
			len(alarms.created), len(store.open))
	}
}

func TestRecoveredClearsAlarmAndClosesEpisode(t *testing.T) {
	store, alarms, sink := &fakeStore{}, &fakeAlarms{}, &fakeSink{}
	s := newSvc(store, alarms, sink)
	s.handle(context.Background(), advJSON(kindDown, "lease_expired"))
	s.handle(context.Background(), advJSON(kindRecovered, "heartbeat"))
	if len(alarms.cleared) != 1 {
		t.Fatalf("expected one clear, got %d", len(alarms.cleared))
	}
	if len(store.closed) != 1 || store.closed[0] != "g1" {
		t.Fatalf("expected episode closed for g1, got %v", store.closed)
	}
}

func TestEscalationSweepBumpsToCritical(t *testing.T) {
	store, alarms, sink := &fakeStore{
		claim: []Episode{{TenantID: "t1", GatewayID: "g1", AlarmID: "al-1"}},
	}, &fakeAlarms{}, &fakeSink{}
	s := newSvc(store, alarms, sink)

	s.escalationSweep(context.Background(), time.Now().UTC())

	if len(alarms.created) != 1 {
		t.Fatalf("expected one escalation alarm, got %d", len(alarms.created))
	}
	if alarms.created[0].Severity != EscalatedSeverity {
		t.Fatalf("severity = %s, want %s", alarms.created[0].Severity, EscalatedSeverity)
	}
}

func TestDigestComposedAndDeliveredOnce(t *testing.T) {
	store := &fakeStore{
		allow:  true,
		emails: []string{"ops@example.com"},
		digRows: []DigestRow{{
			GatewayName: "gw-1", Count: 2, TotalDowntime: 95 * time.Second,
		}},
	}
	sink := &fakeSink{}
	s := newSvc(store, &fakeAlarms{}, sink)

	s.sendDigest(context.Background(), "t1", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.Now().UTC())

	if len(sink.inbox) != 1 {
		t.Fatalf("expected one inbox digest, got %d", len(sink.inbox))
	}
	if sink.sent != 1 {
		t.Fatalf("expected one external delivery, got %d", sink.sent)
	}

	// When MarkDigestSent reports the day was already sent, no delivery.
	store.allow = false
	sink.sent = 0
	sink.inbox = nil
	s.sendDigest(context.Background(), "t1", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.Now().UTC())
	if sink.sent != 0 || len(sink.inbox) != 0 {
		t.Fatal("duplicate digest for the same day must be suppressed")
	}
}

func TestDigestTickOnlyAtConfiguredHour(t *testing.T) {
	store := &fakeStore{
		allow:   true,
		digRows: []DigestRow{{GatewayName: "gw-1", Count: 1}},
	}
	sink := &fakeSink{}
	s := newSvc(store, &fakeAlarms{}, sink)
	wrong := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	s.digestTick(context.Background(), wrong)
	if len(store.marked) != 0 {
		t.Fatal("digest must not send outside the configured hour")
	}
	right := time.Date(2026, 10, 3, 8, 30, 0, 0, time.UTC)
	s.digestTick(context.Background(), right)
	if len(store.marked) != 1 {
		t.Fatal("digest must send at the configured hour")
	}
}

func TestFormatDur(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:            "45s",
		95 * time.Second:            "1m35s",
		3*time.Hour + 5*time.Minute: "3h05m",
	}
	for in, want := range cases {
		if got := formatDur(in); got != want {
			t.Errorf("formatDur(%v) = %q, want %q", in, got, want)
		}
	}
}
