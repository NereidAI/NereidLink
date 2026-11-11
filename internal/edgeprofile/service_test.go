// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

// fakeProfileStore is an in-memory ProfileStore for adopt logic tests.
type fakeProfileStore struct {
	byName map[string]registry.ProfileDTO
	// ingested records every auto-ingest call keyed by profile name.
	ingested map[string]registry.EdgeProfileUpsert
}

func newFakeProfiles() *fakeProfileStore {
	return &fakeProfileStore{
		byName:   map[string]registry.ProfileDTO{},
		ingested: map[string]registry.EdgeProfileUpsert{},
	}
}

func (f *fakeProfileStore) GetProfileByName(_ context.Context, _, name string) (registry.ProfileDTO, error) {
	if p, ok := f.byName[name]; ok {
		return p, nil
	}
	return registry.ProfileDTO{}, registry.ErrNotFound
}

func (f *fakeProfileStore) CreateProfile(_ context.Context, _ string, in registry.ProfileDTO) (registry.ProfileDTO, error) {
	if _, exists := f.byName[in.Name]; exists {
		return registry.ProfileDTO{}, registry.ErrConflict
	}
	in.ID = "native-" + in.Name
	f.byName[in.Name] = in
	return in, nil
}

func (f *fakeProfileStore) UpdateProfile(_ context.Context, _, _ string, in registry.ProfileDTO) (registry.ProfileDTO, error) {
	f.byName[in.Name] = in
	return in, nil
}

// ImportEdgeProfile mimics a first-time create in the in-memory store.
func (f *fakeProfileStore) ImportEdgeProfile(_ context.Context, _ string, in registry.EdgeProfileUpsert) (registry.EdgeProfileUpsertResult, error) {
	if _, exists := f.byName[in.Name]; exists {
		return registry.EdgeProfileUpsertResult{}, registry.ErrConflict
	}
	created, err := f.CreateProfile(context.Background(), "", registry.ProfileDTO{Name: in.Name})
	if err != nil {
		return registry.EdgeProfileUpsertResult{}, err
	}
	return registry.EdgeProfileUpsertResult{ProfileID: created.ID, Created: true, PointCount: len(in.Points)}, nil
}

// UpsertEdgeProfile mimics first-sight create and identical-hash no-op for the
// in-memory store; drift/collision are not exercised through adopt tests.
func (f *fakeProfileStore) UpsertEdgeProfile(_ context.Context, _, _ string, in registry.EdgeProfileUpsert) (registry.EdgeProfileUpsertResult, error) {
	f.ingested[in.Name] = in
	if existing, ok := f.byName[in.Name]; ok {
		return registry.EdgeProfileUpsertResult{ProfileID: existing.ID, Unchanged: true}, nil
	}
	created, err := f.CreateProfile(context.Background(), "", registry.ProfileDTO{Name: in.Name})
	if err != nil {
		return registry.EdgeProfileUpsertResult{}, err
	}
	return registry.EdgeProfileUpsertResult{ProfileID: created.ID, Created: true, PointCount: len(in.Points)}, nil
}

// fakeCache is an in-memory Store replacement implementing only what Adopt
// needs (Get + SetAdoption), avoiding a database in logic tests.
type fakeCache struct {
	profiles map[string]Profile
	meta     map[string]*CachedProfile
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		profiles: map[string]Profile{},
		meta:     map[string]*CachedProfile{},
	}
}

func keyFor(gw, name string) string { return gw + "/" + name }

func (f *fakeCache) put(gw string, p Profile) {
	f.profiles[keyFor(gw, p.Name)] = p
	f.meta[keyFor(gw, p.Name)] = &CachedProfile{GatewayID: gw, Name: p.Name}
}

func (f *fakeCache) Get(_ context.Context, _, gw, name string) (Profile, CachedProfile, error) {
	k := keyFor(gw, name)
	if p, ok := f.profiles[k]; ok {
		return p, *f.meta[k], nil
	}
	return Profile{}, CachedProfile{}, ErrNotFound
}

func (f *fakeCache) SetAdoption(_ context.Context, _, gw, name, nativeID string) error {
	m, ok := f.meta[keyFor(gw, name)]
	if !ok {
		return ErrNotFound
	}
	m.AdoptedProfileID = nativeID
	return nil
}

// Tests construct *Service directly with the in-memory CacheStore and
// ProfileStore doubles; no database is required.

func (f *fakeCache) Report(_ context.Context, _, gw string, p Profile, _ string) error {
	f.profiles[keyFor(gw, p.Name)] = p
	f.meta[keyFor(gw, p.Name)] = &CachedProfile{GatewayID: gw, Name: p.Name}
	return nil
}

func (f *fakeCache) ListForGateway(_ context.Context, _, gw string) ([]CachedProfile, error) {
	out := []CachedProfile{}
	for k, m := range f.meta {
		if strings.HasPrefix(k, gw+"/") {
			out = append(out, *m)
		}
	}
	return out, nil
}

func sampleProfile() Profile {
	return Profile{Name: "Temp", DeviceResources: []Resource{
		{Name: "temp", ValueType: "Float64", Units: "C"},
		{Name: "blob", ValueType: "Binary"},
	}}
}

func TestIngestAutoCreatesLibraryAndLinks(t *testing.T) {
	profiles := newFakeProfiles()
	cache := newFakeCache()
	svc := &Service{store: cache, profiles: profiles}
	ctx := context.Background()

	if err := svc.Ingest(ctx, "tenant", "gw1", Frame{Profiles: []Profile{sampleProfile()}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, ok := profiles.byName["Temp"]; !ok {
		t.Fatal("first sight must auto-create a library profile")
	}
	if profiles.ingested["Temp"].Points[0].DataType != "float" {
		t.Fatalf("neutral mapping wrong: %+v", profiles.ingested["Temp"].Points)
	}
	if cache.meta[keyFor("gw1", "Temp")].AdoptedProfileID == "" {
		t.Fatal("cache row must link to the library profile")
	}
}

func TestIngestSkipsBlankNames(t *testing.T) {
	profiles := newFakeProfiles()
	cache := newFakeCache()
	svc := &Service{store: cache, profiles: profiles}
	blank := Profile{Name: "   ", DeviceResources: []Resource{{Name: "x", ValueType: "Bool"}}}
	if err := svc.Ingest(context.Background(), "tenant", "gw1",
		Frame{Profiles: []Profile{blank, sampleProfile()}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(profiles.ingested) != 1 {
		t.Fatalf("blank name must be skipped, ingested %d", len(profiles.ingested))
	}
}

func TestAdoptDefaultResolvesLibrary(t *testing.T) {
	profiles := newFakeProfiles()
	cache := newFakeCache()
	cache.put("gw1", sampleProfile())
	profiles.byName["Temp"] = registry.ProfileDTO{ID: "lib-id", Name: "Temp"}
	svc := &Service{store: cache, profiles: profiles}

	res, err := svc.Adopt(context.Background(), "tenant",
		AdoptRequest{GatewayID: "gw1", Name: "Temp"})
	if err != nil {
		t.Fatalf("default adopt: %v", err)
	}
	if res.Created || res.Updated || res.ProfileID != "lib-id" {
		t.Fatalf("default adopt must resolve existing library profile: %+v", res)
	}
	if cache.meta[keyFor("gw1", "Temp")].AdoptedProfileID != "lib-id" {
		t.Fatal("default adopt must (re)link the cache row")
	}
}

func TestAdoptUpdateForcesSync(t *testing.T) {
	profiles := newFakeProfiles()
	cache := newFakeCache()
	cache.put("gw1", sampleProfile())
	profiles.byName["Temp"] = registry.ProfileDTO{
		ID: "lib-id", Name: "Temp", ConnectorType: "nereid-edge",
	}
	svc := &Service{store: cache, profiles: profiles}

	res, err := svc.Adopt(context.Background(), "tenant",
		AdoptRequest{GatewayID: "gw1", Name: "Temp", Mode: AdoptUpdate})
	if err != nil {
		t.Fatalf("update adopt: %v", err)
	}
	if !res.Updated || res.ProfileID != "lib-id" {
		t.Fatalf("update adopt must force-sync onto the same library profile: %+v", res)
	}
	if !profiles.ingested["Temp"].Force {
		t.Fatal("update adopt must request a forced upsert over drift")
	}
}

func TestAdoptValidation(t *testing.T) {
	svc := &Service{}
	if _, err := svc.Adopt(context.Background(), "t",
		AdoptRequest{GatewayID: "g", Name: "n", Mode: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mode: %v", err)
	}
	if _, err := svc.Adopt(context.Background(), "t",
		AdoptRequest{Name: "n"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing gateway: %v", err)
	}
}
