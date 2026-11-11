// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers operation catalog registration and merging with
// an in-memory store.

package opcatalog

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeStore is the in-memory catalog store.
type fakeStore struct {
	ads      map[string][]Descriptor // gatewayID -> ordered advertisements
	private  map[string]map[string]Descriptor
	disabled map[string]map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		ads:      map[string][]Descriptor{},
		private:  map[string]map[string]Descriptor{},
		disabled: map[string]map[string]bool{},
	}
}

func key(operation, version string) string { return operation + "|" + version }

func (f *fakeStore) ReplaceGatewayAdvertisements(_ context.Context, gatewayID string, accepted []Descriptor) error {
	copied := make([]Descriptor, len(accepted))
	copy(copied, accepted)
	f.ads[gatewayID] = copied
	return nil
}

func (f *fakeStore) ListGatewayAdvertisements(_ context.Context, _ string, gatewayID string) ([]Descriptor, error) {
	out := make([]Descriptor, len(f.ads[gatewayID]))
	copy(out, f.ads[gatewayID])
	return out, nil
}

func (f *fakeStore) SelectGatewayAdvertisement(_ context.Context, _ string, gatewayID, operation string) (Descriptor, bool, error) {
	// Newest = last accepted row for the operation (replace inserts in order).
	for i := len(f.ads[gatewayID]) - 1; i >= 0; i-- {
		if f.ads[gatewayID][i].Operation == operation {
			return f.ads[gatewayID][i], true, nil
		}
	}
	return Descriptor{}, false, nil
}

func (f *fakeStore) ListTenantAdvertised(_ context.Context, _ string) ([]Advertised, error) {
	// The unit fake has no device table; it treats every gateway as in the
	// queried tenant and sorts by key+gateway for deterministic output.
	out := []Advertised{}
	for gatewayID, descs := range f.ads {
		for _, d := range descs {
			out = append(out, Advertised{
				Descriptor:  d,
				GatewayID:   gatewayID,
				GatewayName: gatewayID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Operation != out[j].Operation {
			return out[i].Operation < out[j].Operation
		}
		if out[i].TypeVersion != out[j].TypeVersion {
			return out[i].TypeVersion < out[j].TypeVersion
		}
		return out[i].GatewayID < out[j].GatewayID
	})
	return out, nil
}

func (f *fakeStore) CreatePrivate(_ context.Context, tenantID string, d Descriptor) error {
	if f.private[tenantID] == nil {
		f.private[tenantID] = map[string]Descriptor{}
	}
	k := key(d.Operation, d.TypeVersion)
	if _, exists := f.private[tenantID][k]; exists {
		return ErrConflict
	}
	f.private[tenantID][k] = d
	return nil
}

func (f *fakeStore) UpdatePrivate(_ context.Context, tenantID, operation, version string, d Descriptor) error {
	rows := f.private[tenantID]
	if rows == nil {
		return ErrNotFound
	}
	if _, ok := rows[key(operation, version)]; !ok {
		return ErrNotFound
	}
	delete(rows, key(operation, version))
	rows[key(d.Operation, d.TypeVersion)] = d
	return nil
}

func (f *fakeStore) DeletePrivate(_ context.Context, tenantID, operation, version string) error {
	rows := f.private[tenantID]
	if rows == nil {
		return ErrNotFound
	}
	if _, ok := rows[key(operation, version)]; !ok {
		return ErrNotFound
	}
	delete(rows, key(operation, version))
	return nil
}

func (f *fakeStore) ListPrivate(_ context.Context, tenantID string) ([]Descriptor, error) {
	out := []Descriptor{}
	for _, d := range f.private[tenantID] {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeStore) SetDisabled(_ context.Context, tenantID, operation, version, _ string) error {
	if f.disabled[tenantID] == nil {
		f.disabled[tenantID] = map[string]bool{}
	}
	f.disabled[tenantID][key(operation, version)] = true
	return nil
}

func (f *fakeStore) ClearDisabled(_ context.Context, tenantID, operation, version string) error {
	delete(f.disabled[tenantID], key(operation, version))
	return nil
}

func (f *fakeStore) DisabledKeys(_ context.Context, tenantID string) (map[string]bool, error) {
	out := map[string]bool{}
	for k := range f.disabled[tenantID] {
		out[k] = true
	}
	return out, nil
}

func TestRegisterFromHelloPartialSuccess(t *testing.T) {
	svc := New(newFakeStore())
	results := svc.RegisterFromHello(context.Background(), "gw-1", []Descriptor{
		{Operation: "reboot", Risk: RiskDangerous},
		{Operation: "bad", Risk: "unknown"},
	})
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	if !results[0].Accepted {
		t.Fatal("reboot should be accepted")
	}
	if results[1].Accepted || results[1].Reason == "" {
		t.Fatal("bad descriptor should be rejected with reason")
	}
}

func TestRegisterFromHelloRejectsEmptyOperation(t *testing.T) {
	svc := New(newFakeStore())
	results := svc.RegisterFromHello(context.Background(), "gw-1",
		[]Descriptor{{Risk: RiskRead}})
	if results[0].Accepted {
		t.Fatal("expected rejection")
	}
}

func TestEffectiveMerge(t *testing.T) {
	store := newFakeStore()
	svc := New(store)
	results := svc.RegisterFromHello(context.Background(), "gw-1", []Descriptor{
		{Operation: "reboot", Risk: RiskDangerous},
		{Operation: "status", Risk: RiskRead},
	})
	if !results[0].Accepted {
		t.Fatal(results[0].Reason)
	}

	if err := svc.SetDisabled(context.Background(), "t1", "reboot", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateManual(context.Background(), "t1",
		Descriptor{Operation: "status", Risk: RiskNormal}); err != nil {
		t.Fatal(err)
	}

	views, err := svc.Effective(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]View{}
	for _, v := range views {
		byKey[key(v.Operation, v.TypeVersion)] = v
	}
	if !byKey[key("reboot", "")].Disabled {
		t.Fatal("reboot should be disabled")
	}
	if byKey[key("status", "")].Source != SourceManual ||
		byKey[key("status", "")].Risk != RiskNormal {
		t.Fatal("private descriptor should override global")
	}
}

func TestCloneToPrivate(t *testing.T) {
	svc := New(newFakeStore())
	svc.RegisterFromHello(context.Background(), "gw-1",
		[]Descriptor{{Operation: "reboot", Risk: RiskDangerous}})
	if err := svc.CloneToPrivate(context.Background(), "t1", "reboot", "", "reboot-custom"); err != nil {
		t.Fatal(err)
	}
	views, _ := svc.Effective(context.Background(), "t1")
	found := false
	for _, v := range views {
		if v.Operation == "reboot-custom" && v.Source == SourceManual {
			found = true
		}
	}
	if !found {
		t.Fatal("clone not found")
	}
}

func TestCloneMissingOperation(t *testing.T) {
	svc := New(newFakeStore())
	if err := svc.CloneToPrivate(context.Background(), "t1", "nope", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// TestPerGatewayContractsDoNotBleed proves two gateways advertising the same
// key with different schemas/risk resolve their OWN descriptor at invocation.
func TestPerGatewayContractsDoNotBleed(t *testing.T) {
	svc := New(newFakeStore())
	svc.RegisterFromHello(context.Background(), "gw-A", []Descriptor{
		{Operation: "diag.ping", Risk: RiskRead, TimeoutMs: 5000,
			ParamSchema: []byte(`{"type":"object"}`)},
	})
	svc.RegisterFromHello(context.Background(), "gw-B", []Descriptor{
		{Operation: "diag.ping", Risk: RiskDangerous, TimeoutMs: 9000,
			ParamSchema: []byte(`{"type":"object","required":["host"]}`)},
	})

	a, ok, err := svc.EffectiveForGateway(context.Background(), "t1", "gw-A", "diag.ping")
	if err != nil || !ok {
		t.Fatalf("A lookup ok=%v err=%v", ok, err)
	}
	b, _, _ := svc.EffectiveForGateway(context.Background(), "t1", "gw-B", "diag.ping")
	if a.Risk != RiskRead || a.TimeoutMs != 5000 {
		t.Fatalf("A contract bled: %+v", a.Descriptor)
	}
	if b.Risk != RiskDangerous || b.TimeoutMs != 9000 {
		t.Fatalf("B contract bled: %+v", b.Descriptor)
	}
}

// TestSelectNewestAdvertisedVersion proves invocation picks the newest
// version row when one gateway advertises multiple versions.
func TestSelectNewestAdvertisedVersion(t *testing.T) {
	svc := New(newFakeStore())
	svc.RegisterFromHello(context.Background(), "gw-A", []Descriptor{
		{Operation: "op", TypeVersion: "1", Risk: RiskRead, TimeoutMs: 1000},
		{Operation: "op", TypeVersion: "2", Risk: RiskRead, TimeoutMs: 2000},
	})
	view, ok, err := svc.EffectiveForGateway(context.Background(), "t1", "gw-A", "op")
	if err != nil || !ok {
		t.Fatalf("lookup ok=%v err=%v", ok, err)
	}
	if view.TypeVersion != "2" || view.TimeoutMs != 2000 {
		t.Fatalf("expected newest v2, got %s/%dms", view.TypeVersion, view.TimeoutMs)
	}
}

// TestFullSetReconcileRemoves proves a later hello without a previously
// advertised key removes it for that gateway only.
func TestFullSetReconcileRemoves(t *testing.T) {
	svc := New(newFakeStore())
	svc.RegisterFromHello(context.Background(), "gw-A", []Descriptor{
		{Operation: "a", Risk: RiskRead}, {Operation: "b", Risk: RiskRead},
	})
	svc.RegisterFromHello(context.Background(), "gw-A", []Descriptor{
		{Operation: "a", Risk: RiskRead},
	})
	svc.RegisterFromHello(context.Background(), "gw-B", []Descriptor{
		{Operation: "b", Risk: RiskRead},
	})
	if _, ok, _ := svc.EffectiveForGateway(context.Background(), "t1", "gw-A", "b"); ok {
		t.Fatal("b should have been reconciled away for gw-A")
	}
	if _, ok, _ := svc.EffectiveForGateway(context.Background(), "t1", "gw-B", "b"); !ok {
		t.Fatal("b must remain for gw-B")
	}
}

// TestPrivateFallbackForNonAdvertisingGateway proves a tenant-private
// descriptor is invocable on a gateway that does not advertise the key.
func TestPrivateFallbackForNonAdvertisingGateway(t *testing.T) {
	svc := New(newFakeStore())
	if err := svc.CreateManual(context.Background(), "t1",
		Descriptor{Operation: "custom.run", Risk: RiskNormal, TimeoutMs: 4000}); err != nil {
		t.Fatal(err)
	}
	view, ok, err := svc.EffectiveForGateway(context.Background(), "t1", "gw-X", "custom.run")
	if err != nil || !ok {
		t.Fatalf("private fallback ok=%v err=%v", ok, err)
	}
	if view.Source != SourceManual {
		t.Fatalf("source=%s", view.Source)
	}
}

// TestAggregateFlagsConflictAndAdvertisers proves the console aggregate lists
// advertisers and flags divergent same-key contracts.
func TestAggregateFlagsConflictAndAdvertisers(t *testing.T) {
	svc := New(newFakeStore())
	svc.RegisterFromHello(context.Background(), "gw-A", []Descriptor{
		{Operation: "diag.ping", Risk: RiskRead, TimeoutMs: 5000},
	})
	svc.RegisterFromHello(context.Background(), "gw-B", []Descriptor{
		{Operation: "diag.ping", Risk: RiskRead, TimeoutMs: 8000},
	})
	views, err := svc.Effective(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Operation == "diag.ping" {
			if !v.Conflict {
				t.Fatal("expected conflict flag for divergent timeout")
			}
			if v.GatewayCount != 2 || len(v.AdvertisedBy) != 2 {
				t.Fatalf("advertisers=%v", v.AdvertisedBy)
			}
			return
		}
	}
	t.Fatal("diag.ping missing from aggregate")
}
