// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/presence"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDesired returns a fixed desired (bound) set.
type fakeDesired struct{ ids []string }

func (f fakeDesired) ListGatewaySubdevices(_ context.Context, _, _ string) ([]string, error) {
	return f.ids, nil
}

// fakePresence records presence convergence actions.
type fakePresence struct {
	online  map[string]bool
	offline []string
	reasons []string
}

func newFakePresence() *fakePresence {
	return &fakePresence{online: map[string]bool{}}
}

func (f *fakePresence) EnsureGatewayOnline(_ context.Context, id presence.Identity, _ string) (bool, error) {
	if f.online[id.DeviceID] {
		return false, nil
	}
	f.online[id.DeviceID] = true
	return true, nil
}

func (f *fakePresence) EnsureGatewayOffline(_ context.Context, id presence.Identity, _, reason string) (bool, error) {
	if !f.online[id.DeviceID] {
		return false, nil
	}
	delete(f.online, id.DeviceID)
	f.offline = append(f.offline, id.DeviceID)
	f.reasons = append(f.reasons, reason)
	return true, nil
}

func (f *fakePresence) DeriveGatewayOffline(_ context.Context, _, _ string, reason string) (int, error) {
	n := 0
	for id := range f.online {
		delete(f.online, id)
		f.offline = append(f.offline, id)
		f.reasons = append(f.reasons, reason)
		n++
	}
	return n, nil
}

// fakeLease records the last Put per key.
type fakeLease struct{ puts map[string][]byte }

func (f *fakeLease) Put(_ context.Context, key string, value []byte) (uint64, error) {
	f.puts[key] = value
	return 1, nil
}

// fakePublisher records published advisories.
type fakePublisher struct {
	subjects []string
	payloads [][]byte
}

func (f *fakePublisher) Publish(subject string, data []byte) error {
	f.subjects = append(f.subjects, subject)
	f.payloads = append(f.payloads, data)
	return nil
}

func newTestReconciler(desired []string) (*Reconciler, *fakePresence, *fakeLease) {
	pr := newFakePresence()
	lease := &fakeLease{puts: map[string][]byte{}}
	r := New(nil, nil, nil, fakeDesired{ids: desired}, pr,
		Tuning{}, testLogger())
	r.leases = lease
	return r, pr, lease
}

func TestReconcileConvergesPresenceAndRefreshesLease(t *testing.T) {
	// desired: a(in sync), b(missing), c(missing). reported: a + rogue d.
	r, pr, lease := newTestReconciler([]string{"a", "b", "c"})

	report, err := r.Reconcile(context.Background(), "tenant", "gw", Frame{Devices: []string{"a", "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.OnlinePromoted != 1 || !pr.online["a"] {
		t.Fatalf("expected a promoted online, got %+v", pr.online)
	}
	// b and c were never attached, so missing produces no boundary.
	if report.OfflineDerived != 0 {
		t.Fatalf("expected 0 boundaries for already-offline devices, got %d", report.OfflineDerived)
	}
	if got := report.Class.Unregistered; len(got) != 1 || got[0] != "d" {
		t.Fatalf("expected d unregistered, got %v", got)
	}
	if _, ok := lease.puts["tenant.gw"]; !ok {
		t.Fatal("expected lease refresh")
	}
}

func TestReconcileMissingDeviceGoesOffline(t *testing.T) {
	r, pr, _ := newTestReconciler([]string{"a", "b"})
	ctx := context.Background()

	// First manifest reports both: both become online.
	if _, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	// Second manifest drops b: b must derive an OFFLINE boundary.
	report, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.OfflineDerived != 1 {
		t.Fatalf("expected one offline boundary, got %d", report.OfflineDerived)
	}
	sort.Strings(pr.offline)
	if len(pr.offline) != 1 || pr.offline[0] != "b" {
		t.Fatalf("expected b offline, got %v", pr.offline)
	}
	if pr.reasons[0] != presence.ReasonManifest {
		t.Fatalf("expected manifest reason, got %q", pr.reasons[0])
	}
	if pr.online["b"] {
		t.Fatal("b must no longer be online")
	}
}

func TestReconcileIdempotentNoBoundary(t *testing.T) {
	r, _, _ := newTestReconciler([]string{"a"})
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	report, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.OnlinePromoted != 0 || report.OfflineDerived != 0 || report.Class.Drift() {
		t.Fatalf("steady manifest must be a no-op, got %+v", report)
	}
}

func TestDeriveExpiredDrivesAttachedOffline(t *testing.T) {
	r, pr, _ := newTestReconciler([]string{"a", "b"})
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if len(pr.online) != 2 {
		t.Fatalf("expected both online, got %v", pr.online)
	}

	if !r.deriveExpired(ctx, "tenant.gw", "test") {
		t.Fatal("expected lease expiry to be handled")
	}

	if len(pr.online) != 0 {
		t.Fatalf("expected all derived offline, got %v", pr.online)
	}
	if len(pr.offline) != 2 {
		t.Fatalf("expected two offline boundaries, got %v", pr.offline)
	}
	if pr.reasons[0] != presence.ReasonManifestLeaseExpired {
		t.Fatalf("expected lease-expired reason, got %q", pr.reasons[0])
	}
}

func TestDeriveExpiredIgnoresMalformedKey(t *testing.T) {
	r, pr, _ := newTestReconciler([]string{"a"})
	if r.deriveExpired(context.Background(), "nodot", "test") {
		t.Fatal("malformed lease key must be ignored")
	}
	if len(pr.offline) != 0 {
		t.Fatal("malformed lease key must not derive offline")
	}
}

func TestDriftAdvisoryPublished(t *testing.T) {
	r, _, _ := newTestReconciler([]string{"a", "b"})
	pub := &fakePublisher{}
	r.publisher = pub

	// Report only a: b is missing -> drift advisory expected.
	if _, err := r.Reconcile(context.Background(), "tenant", "gw",
		Frame{Devices: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(pub.subjects) != 1 {
		t.Fatalf("expected one drift advisory, got %d", len(pub.subjects))
	}
	if want := "nl.manifest.tenant.gw"; pub.subjects[0] != want {
		t.Fatalf("subject = %q, want %q", pub.subjects[0], want)
	}
	var adv Advisory
	if err := json.Unmarshal(pub.payloads[0], &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Kind != AdvisoryDrift || len(adv.Missing) != 1 || adv.Missing[0] != "b" {
		t.Fatalf("unexpected advisory: %+v", adv)
	}
}

func TestNoDriftNoAdvisory(t *testing.T) {
	r, _, _ := newTestReconciler([]string{"a"})
	pub := &fakePublisher{}
	r.publisher = pub
	if _, err := r.Reconcile(context.Background(), "tenant", "gw",
		Frame{Devices: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(pub.subjects) != 0 {
		t.Fatalf("in-sync manifest must not publish an advisory, got %d", len(pub.subjects))
	}
}

func TestLeaseExpiredAdvisoryPublished(t *testing.T) {
	r, _, _ := newTestReconciler([]string{"a", "b"})
	pub := &fakePublisher{}
	r.publisher = pub
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, "tenant", "gw", Frame{Devices: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if !r.deriveExpired(ctx, "tenant.gw", "test") {
		t.Fatal("expected lease expiry handled")
	}
	if len(pub.subjects) != 1 {
		t.Fatalf("expected one lease-expired advisory, got %d", len(pub.subjects))
	}
	var adv Advisory
	if err := json.Unmarshal(pub.payloads[0], &adv); err != nil {
		t.Fatal(err)
	}
	if adv.Kind != AdvisoryLeaseExpired || adv.OfflineDerived != 2 {
		t.Fatalf("unexpected lease-expired advisory: %+v", adv)
	}
}

func TestExplicitDeleteNotReDerivedBySweep(t *testing.T) {
	tracker := newLeaseTracker()
	key := "tenant.gw"
	now := time.Now().UTC()
	tracker.observe(key, now)
	if !tracker.shouldDerive(key, now.Add(time.Hour), time.Minute) {
		t.Fatal("first expiry must derive")
	}
	// Mark as an explicit watcher deletion handled; a later sweep must
	// not re-derive the same absent key.
	tracker.markDerived(key)
	if tracker.shouldDerive(key, now.Add(2*time.Hour), time.Minute) {
		t.Fatal("already-derived explicit deletion must not re-derive")
	}
	// A fresh sighting re-arms the tracker.
	tracker.observe(key, now.Add(3*time.Hour))
	if !tracker.shouldDerive(key, now.Add(5*time.Hour), time.Minute) {
		t.Fatal("resumed gateway that expires again must re-derive")
	}
}
