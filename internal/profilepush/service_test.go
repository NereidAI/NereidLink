// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go exercises the delivery state machine against an in-memory
// store stub: distribute creates a PENDING desired row and publishes, a
// status uplink moves it to APPLIED, a profile content change re-opens it as
// PENDING, and withdrawal falls back to REMOVING when no connector reference
// remains.

package profilepush

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeStore is a minimal in-memory deliveryStore for the service tests.
type fakeStore struct {
	mu         sync.Mutex
	profiles   map[string]LibraryProfile
	targets    map[string]map[string]bool // profileID -> gatewayID set
	refs       map[string]map[string]bool // gatewayID -> profileID set
	deliveries map[string]Delivery        // profileID|gatewayID -> row
	formats    map[string][]string        // gatewayID -> declared formats
	services   map[string]string          // protocol -> service name
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		profiles:   map[string]LibraryProfile{},
		targets:    map[string]map[string]bool{},
		refs:       map[string]map[string]bool{},
		deliveries: map[string]Delivery{},
		formats:    map[string][]string{},
		services:   map[string]string{},
	}
}

func dkey(profileID, gatewayID string) string { return profileID + "|" + gatewayID }

func (f *fakeStore) LibraryProfile(_ context.Context, _, profileID string) (LibraryProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[profileID]
	if !ok {
		return LibraryProfile{}, ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) LibraryProfileByName(_ context.Context, _, name string) (LibraryProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return LibraryProfile{}, ErrNotFound
}

func (f *fakeStore) AddTarget(_ context.Context, _, profileID, gatewayID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.targets[profileID] == nil {
		f.targets[profileID] = map[string]bool{}
	}
	f.targets[profileID][gatewayID] = true
	return nil
}

func (f *fakeStore) RemoveTarget(_ context.Context, _, profileID, gatewayID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.targets[profileID], gatewayID)
	return nil
}

func (f *fakeStore) ListTargets(_ context.Context, _, profileID string) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Target{}
	for gw := range f.targets[profileID] {
		out = append(out, Target{ProfileID: profileID, GatewayID: gw})
	}
	return out, nil
}

func (f *fakeStore) ReferencingGateways(_ context.Context, _, profileID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for gw, set := range f.refs {
		if set[profileID] {
			out = append(out, gw)
		}
	}
	return out, nil
}

func (f *fakeStore) ReferencedProfiles(_ context.Context, _, gatewayID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for id := range f.refs[gatewayID] {
		out = append(out, id)
	}
	return out, nil
}

func (f *fakeStore) TargetedProfiles(_ context.Context, _, gatewayID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for id, set := range f.targets {
		if set[gatewayID] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeStore) SyncDesired(_ context.Context, _, profileID string, revision int64, gatewayIDs []string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, gw := range gatewayIDs {
		row := f.deliveries[dkey(profileID, gw)]
		if row.DesiredRevision != revision {
			row.State = StatePending
		}
		row.ProfileID = profileID
		row.GatewayID = gw
		row.ProfileName = f.profiles[profileID].Name
		row.DesiredRevision = revision
		row.DesiredAt = at
		f.deliveries[dkey(profileID, gw)] = row
	}
	return nil
}

func (f *fakeStore) MarkRemoving(_ context.Context, _, profileID, gatewayID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.deliveries[dkey(profileID, gatewayID)]
	row.State = StateRemoving
	f.deliveries[dkey(profileID, gatewayID)] = row
	return nil
}

func (f *fakeStore) DeliveriesByGateway(_ context.Context, _, gatewayID string) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Delivery{}
	for _, row := range f.deliveries {
		if row.GatewayID == gatewayID && row.State != StateRemoved {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) DeliveriesByProfile(_ context.Context, _, profileID string) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Delivery{}
	for _, row := range f.deliveries {
		if row.ProfileID == profileID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) MarkDispatched(_ context.Context, _, profileID, gatewayID string, revision int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.deliveries[dkey(profileID, gatewayID)]
	if !ok || row.DesiredRevision != revision {
		return nil
	}
	row.State = StateDispatched
	row.DispatchedRevision = revision
	f.deliveries[dkey(profileID, gatewayID)] = row
	return nil
}

func (f *fakeStore) Report(_ context.Context, _, gatewayID string, report StatusEntry, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.deliveries {
		if row.GatewayID != gatewayID || row.ProfileName != report.Name {
			continue
		}
		row.State = report.Status
		row.Reason = report.Reason
		if report.Status == StateApplied {
			row.AppliedRevision = report.Revision
			row.AppliedAt = &at
		}
		f.deliveries[key] = row
		return nil
	}
	return ErrNotFound
}

func (f *fakeStore) BumpRevision(_ context.Context, _, profileID, desiredHash string, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[profileID]
	if !ok {
		return 0, ErrNotFound
	}
	if p.DesiredHash != desiredHash {
		p.Revision++
		p.DesiredHash = desiredHash
		f.profiles[profileID] = p
	}
	if p.Revision == 0 {
		p.Revision = 1
		f.profiles[profileID] = p
	}
	return f.profiles[profileID].Revision, nil
}

func (f *fakeStore) GatewayFormats(_ context.Context, _, gatewayID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.formats[gatewayID], nil
}

func (f *fakeStore) ServiceNameForProtocol(_ context.Context, protocol string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.services[protocol], nil
}

func (f *fakeStore) ClaimPendingForAlarm(context.Context, time.Time) ([]Delivery, error) {
	return nil, nil
}

func (f *fakeStore) ClaimFailedForRaise(context.Context) ([]Delivery, error) { return nil, nil }

func (f *fakeStore) ClaimFailedForEscalation(context.Context, time.Time) ([]Delivery, error) {
	return nil, nil
}

// capturePublisher records published frames by subject.
type capturePublisher struct {
	mu     sync.Mutex
	frames map[string][]byte
}

func newCapturePublisher() *capturePublisher {
	return &capturePublisher{frames: map[string][]byte{}}
}

func (c *capturePublisher) Publish(subject string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames[subject] = append([]byte(nil), data...)
	return nil
}

func (c *capturePublisher) last(subject string) (PushFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, ok := c.frames[subject]
	if !ok {
		return PushFrame{}, false
	}
	var frame PushFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return PushFrame{}, false
	}
	return frame, true
}

// addProfile seeds a library profile.
func (f *fakeStore) addProfile(id, name, protocol string, points ...Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[id] = LibraryProfile{
		ID: id, Name: name, Protocol: protocol, Points: points,
	}
}

// setProfilePoints updates only the points of an existing profile, preserving
// the revision/hash the way a real UPDATE would.
func (f *fakeStore) setProfilePoints(id string, points ...Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.profiles[id]
	p.Points = points
	f.profiles[id] = p
}

// TestDistributeDeliversAndApplies walks the happy path.
func TestDistributeDeliversAndApplies(t *testing.T) {
	store := newFakeStore()
	pub := newCapturePublisher()
	svc := New(store, pub)
	ctx := context.Background()

	store.addProfile("p1", "A1", "modbus",
		Point{Name: "temperature", Type: "float"})
	store.formats["g1"] = []string{FormatEdgeX}

	if err := svc.Distribute(ctx, "t1", "p1", "g1"); err != nil {
		t.Fatalf("distribute: %v", err)
	}

	subject := SubjectDesiredPrefix + ".t1.g1"
	frame, ok := pub.last(subject)
	if !ok {
		t.Fatalf("no frame published on %s", subject)
	}
	if len(frame.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(frame.Entries))
	}
	entry := frame.Entries[0]
	if entry.Name != "A1" || entry.ProfileID != "p1" {
		t.Fatalf("unexpected entry identity: %+v", entry)
	}
	if entry.Format != FormatEdgeX {
		t.Fatalf("gateway preference ignored: %q", entry.Format)
	}
	if _, ok := entry.Document["deviceResources"]; !ok {
		t.Fatalf("EdgeX document not rendered: %+v", entry.Document)
	}

	// The row is PENDING until the edge reports.
	rows, _ := svc.DeliveriesForGateway(ctx, "t1", "g1")
	if len(rows) != 1 || rows[0].State != StatePending {
		t.Fatalf("expected PENDING delivery, got %+v", rows)
	}

	// Dispatch marking then an APPLIED report.
	if err := svc.MarkDispatched(ctx, "t1", "g1", "p1", entry.Revision); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	rows, _ = svc.DeliveriesForGateway(ctx, "t1", "g1")
	if rows[0].State != StateDispatched {
		t.Fatalf("expected DISPATCHED, got %q", rows[0].State)
	}
	if err := svc.HandleStatus(ctx, "t1", "g1", StatusFrame{Reports: []StatusEntry{
		{Name: "A1", Revision: entry.Revision, Status: StateApplied},
	}}); err != nil {
		t.Fatalf("handle status: %v", err)
	}
	rows, _ = svc.DeliveriesForGateway(ctx, "t1", "g1")
	if rows[0].State != StateApplied {
		t.Fatalf("expected APPLIED, got %q", rows[0].State)
	}
}

// TestContentChangeReopensDelivery asserts a point change bumps the revision
// and re-opens a previously APPLIED delivery as PENDING.
func TestContentChangeReopensDelivery(t *testing.T) {
	store := newFakeStore()
	pub := newCapturePublisher()
	svc := New(store, pub)
	ctx := context.Background()

	store.addProfile("p1", "A1", "modbus", Point{Name: "temperature", Type: "float"})
	if err := svc.Distribute(ctx, "t1", "p1", "g1"); err != nil {
		t.Fatalf("distribute: %v", err)
	}
	rows, _ := svc.DeliveriesForGateway(ctx, "t1", "g1")
	rev := rows[0].DesiredRevision
	if err := svc.HandleStatus(ctx, "t1", "g1", StatusFrame{Reports: []StatusEntry{
		{Name: "A1", Revision: rev, Status: StateApplied},
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Change an edge-visible field and reconcile.
	store.setProfilePoints("p1",
		Point{Name: "temperature", Type: "float"}, Point{Name: "setpoint", Type: "int"})
	if err := svc.ReconcileProfile(ctx, "t1", "p1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rows, _ = svc.DeliveriesForGateway(ctx, "t1", "g1")
	if rows[0].DesiredRevision <= rev {
		t.Fatalf("revision not bumped: %d <= %d", rows[0].DesiredRevision, rev)
	}
	if rows[0].State != StatePending {
		t.Fatalf("expected PENDING after content change, got %q", rows[0].State)
	}
}

// TestWithdrawFallsBackToReference asserts withdrawal keeps the profile when a
// connector instance still references it, and removes it otherwise.
func TestWithdrawFallsBackToReference(t *testing.T) {
	store := newFakeStore()
	pub := newCapturePublisher()
	svc := New(store, pub)
	ctx := context.Background()

	store.addProfile("p1", "A1", "modbus", Point{Name: "temperature", Type: "float"})
	if err := svc.Distribute(ctx, "t1", "p1", "g1"); err != nil {
		t.Fatalf("distribute: %v", err)
	}

	// A connector instance still references the profile: withdrawal from the
	// explicit list must not remove it.
	store.refs["g1"] = map[string]bool{"p1": true}
	if err := svc.WithdrawTarget(ctx, "t1", "p1", "g1"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	rows, _ := svc.DeliveriesForGateway(ctx, "t1", "g1")
	if len(rows) != 1 || rows[0].State == StateRemoving {
		t.Fatalf("profile wrongly removed while referenced: %+v", rows)
	}

	// Drop the reference: the next withdrawal sends a removal frame.
	store.refs["g1"] = map[string]bool{}
	if err := svc.WithdrawTarget(ctx, "t1", "p1", "g1"); err != nil {
		t.Fatalf("withdraw again: %v", err)
	}
	rows, _ = svc.DeliveriesForGateway(ctx, "t1", "g1")
	if len(rows) != 1 || rows[0].State != StateRemoving {
		t.Fatalf("expected REMOVING, got %+v", rows)
	}
	frame, ok := pub.last(SubjectDesiredPrefix + ".t1.g1")
	if !ok || len(frame.Entries) != 1 || !frame.Entries[0].Removing {
		t.Fatalf("removal frame not published: %+v", frame)
	}
	if frame.Entries[0].Document != nil {
		t.Fatal("removal frame must not carry a document")
	}
}

// TestUnknownStatusRejected asserts an unknown uplink status is refused.
func TestUnknownStatusRejected(t *testing.T) {
	svc := New(newFakeStore(), newCapturePublisher())
	err := svc.HandleStatus(context.Background(), "t1", "g1", StatusFrame{Reports: []StatusEntry{
		{Name: "A1", Revision: 1, Status: "BOGUS"},
	}})
	if !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("expected ErrUnknownStatus, got %v", err)
	}
}
