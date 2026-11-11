// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profile_spec_db_test.go verifies the ADR-0032 ownership split for the
// neutral point-spec columns through the operator-facing profile API:
//
//   - A console-authored (manual) profile accepts data_type/read_write/
//     address/protocol on create and on subsequent updates.
//   - On an edge/import-owned profile the protocol family and each point's
//     connector address are operator-maintainable (a discovery/import frame
//     commonly omits both), while the vendor spec columns
//     (data_type/read_write/edge_default/edge_min/edge_max) stay owned by the
//     report/import: a console update must not clobber them.
//   - A replayed edge frame that fills none of the spec fields (blank family,
//     empty {} address, blank scalars) never clobbers stored values; only
//     populated fields are adopted.
package registry

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

const profileSpecTestTenant = "00000000-0000-0000-0000-00000000d758"

func resetProfileSpecFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{
		"profile_gateway_source",
		"measurement_descriptor",
		"device_profile",
	} {
		_, _ = pool.Exec(ctx,
			"DELETE FROM "+table+" WHERE tenant_id=$1", profileSpecTestTenant)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'profile-spec-test')
		 ON CONFLICT (id) DO NOTHING`, profileSpecTestTenant); err != nil {
		t.Fatal(err)
	}
}

// specDescriptor is a descriptor carrying the neutral spec fields under
// test.
func specDescriptor(key, dataType, readWrite string, address map[string]any) DescriptorDTO {
	raw, _ := json.Marshal(address)
	return DescriptorDTO{
		MeasurementKey: key,
		TransformType:  "identity",
		Scale:          1,
		Precision:      -1,
		DataType:       dataType,
		ReadWrite:      readWrite,
		Address:        raw,
	}
}

// TestManualProfileSpecEditable verifies create + update persist spec
// columns and protocol on a manual profile.
func TestManualProfileSpecEditable(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(profileSpecTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetProfileSpecFixture(t, ctx, pool)
	t.Cleanup(func() { resetProfileSpecFixture(t, context.Background(), pool) })

	created, err := admin.CreateProfile(ctx, profileSpecTestTenant, ProfileDTO{
		Name:     "manual-spec-profile",
		Protocol: "modbus",
		Descriptors: []DescriptorDTO{
			specDescriptor("temp", "float", "R", map[string]any{
				"primaryTable":    "HOLDING_REGISTERS",
				"startingAddress": 0,
			}),
		},
	})
	if err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if created.Protocol != "modbus" {
		t.Fatalf("protocol must persist on create, got %q", created.Protocol)
	}
	d := created.Descriptors[0]
	if d.DataType != "float" || d.ReadWrite != "R" {
		t.Fatalf("spec fields must persist on create, got %+v", d)
	}

	// Update: change spec + calibration; both must land.
	updated := DescriptorDTO{
		ID:             d.ID,
		MeasurementKey: "temp",
		TransformType:  "linear",
		Scale:          2,
		Offset:         1,
		Precision:      -1,
		DataType:       "int",
		ReadWrite:      "RW",
		Address:        json.RawMessage(`{"primaryTable":"INPUT_REGISTERS","startingAddress":4}`),
	}
	saved, err := admin.UpdateProfile(ctx, profileSpecTestTenant, created.ID, ProfileDTO{
		Name:        "manual-spec-profile",
		Protocol:    "opcua",
		Descriptors: []DescriptorDTO{updated},
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if saved.Protocol != "opcua" {
		t.Fatalf("protocol must change on manual update, got %q", saved.Protocol)
	}
	got := saved.Descriptors[0]
	if got.DataType != "int" || got.ReadWrite != "RW" {
		t.Fatalf("spec fields must update on manual profile, got %+v", got)
	}
	if got.Version != 2 {
		t.Fatalf("a real edit must bump descriptor version, got %d", got.Version)
	}
}

// TestEdgeProfileSpecProtected verifies a console update against an
// edge-owned profile: the protocol family and point address ARE operator
// maintainable (the discovery frame omitted a usable binding), but the vendor
// spec columns (data_type/read_write) are preserved and a calibration edit
// still persists.
func TestEdgeProfileSpecProtected(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(profileSpecTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetProfileSpecFixture(t, ctx, pool)
	t.Cleanup(func() { resetProfileSpecFixture(t, context.Background(), pool) })

	// Seed an edge-owned profile exactly as the auto-ingest path would.
	_, err = admin.UpsertEdgeProfile(ctx, profileSpecTestTenant, "", EdgeProfileUpsert{
		Name:        "edge-spec-profile",
		Protocol:    "modbus",
		Payload:     json.RawMessage(`{"name":"edge-spec-profile","protocol":"modbus","points":[{"name":"flow","type":"float"}]}`),
		ContentHash: "seed-hash-1",
		Points: []EdgePoint{{
			Name:      "flow",
			DataType:  "float",
			ReadWrite: "R",
			Address:   json.RawMessage(`{"primaryTable":"COILS","startingAddress":9}`),
		}},
	})
	if err != nil {
		t.Fatalf("UpsertEdgeProfile: %v", err)
	}
	current, err := admin.GetProfileByName(ctx, profileSpecTestTenant, "edge-spec-profile")
	if err != nil {
		t.Fatalf("GetProfileByName: %v", err)
	}
	if current.Source != SourceEdge {
		t.Fatalf("test fixture must be source=edge, got %q", current.Source)
	}
	original := current.Descriptors[0]
	if original.DataType != "float" || original.Version != 1 {
		t.Fatalf("unexpected seeded descriptor: %+v", original)
	}

	// The console form fills the operator-owned protocol family and point
	// address, applies a calibration edit, and echoes the vendor spec
	// (data_type/read_write) which must be ignored.
	_, err = admin.UpdateProfile(ctx, profileSpecTestTenant, current.ID, ProfileDTO{
		Name:     "edge-spec-profile",
		Protocol: "onvif",
		Descriptors: []DescriptorDTO{{
			ID:             original.ID,
			MeasurementKey: "flow",
			TransformType:  "linear",
			Scale:          10,
			Offset:         0,
			Unit:           "L/s",
			Precision:      -1,
			DataType:       "string",
			ReadWrite:      "RW",
			Address:        json.RawMessage(`{"service":"Media","function":"GetProfiles"}`),
		}},
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	stored, err := admin.GetProfile(ctx, profileSpecTestTenant, current.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if stored.Protocol != "onvif" {
		t.Fatalf("operator must be able to set protocol family on edge profile, got %q", stored.Protocol)
	}
	got := stored.Descriptors[0]
	if got.DataType != "float" || got.ReadWrite != "R" {
		t.Fatalf("vendor spec columns must survive a console update, got %+v", got)
	}
	var address map[string]any
	if err := json.Unmarshal(got.Address, &address); err != nil {
		t.Fatalf("address must stay valid JSON: %v", err)
	}
	if address["service"] != "Media" {
		t.Fatalf("operator address edit must persist, got %v", address)
	}
	if got.Scale != 10 || got.TransformType != "linear" {
		t.Fatalf("calibration edits must persist, got %+v", got)
	}
	if got.Version != 2 {
		t.Fatalf("a real edit (address/calibration) must bump version exactly once, got %d", got.Version)
	}
}

// TestEdgeDriftEmptyFrameNoClobber verifies that accepting a minimal edge
// report which fills no spec fields never clobbers the operator-maintained
// protocol family and point address, nor the previously reported spec.
func TestEdgeDriftEmptyFrameNoClobber(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(profileSpecTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetProfileSpecFixture(t, ctx, pool)
	t.Cleanup(func() { resetProfileSpecFixture(t, context.Background(), pool) })

	// First report: fully populated.
	_, err = admin.UpsertEdgeProfile(ctx, profileSpecTestTenant, "", EdgeProfileUpsert{
		Name:        "edge-drift-profile",
		Protocol:    "modbus",
		Payload:     json.RawMessage(`{"v":1}`),
		ContentHash: "h1",
		Points: []EdgePoint{{
			Name:      "flow",
			DataType:  "float",
			Unit:      "L/s",
			ReadWrite: "R",
			Address:   json.RawMessage(`{"primaryTable":"COILS","startingAddress":9}`),
		}},
	})
	if err != nil {
		t.Fatalf("seed UpsertEdgeProfile: %v", err)
	}

	// Operator maintains the protocol family and point address by hand.
	current, err := admin.GetProfileByName(ctx, profileSpecTestTenant, "edge-drift-profile")
	if err != nil {
		t.Fatalf("GetProfileByName: %v", err)
	}
	original := current.Descriptors[0]
	if _, err := admin.UpdateProfile(ctx, profileSpecTestTenant, current.ID, ProfileDTO{
		Name:     "edge-drift-profile",
		Protocol: "onvif",
		Descriptors: []DescriptorDTO{{
			ID:             original.ID,
			MeasurementKey: "flow",
			TransformType:  "identity",
			Scale:          1,
			Unit:           "L/s",
			Precision:      -1,
			Address:        json.RawMessage(`{"service":"Media"}`),
		}},
	}); err != nil {
		t.Fatalf("operator UpdateProfile: %v", err)
	}

	// A later minimal discovery frame: blank family, blank scalars, empty {}
	// address. Force accepts the drift; nothing operator/spec-owned may be
	// lost.
	res, err := admin.UpsertEdgeProfile(ctx, profileSpecTestTenant, "", EdgeProfileUpsert{
		Name:        "edge-drift-profile",
		Protocol:    "",
		Payload:     json.RawMessage(`{"v":2}`),
		ContentHash: "h2",
		Force:       true,
		Points: []EdgePoint{{
			Name:    "flow",
			Address: json.RawMessage(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("drift UpsertEdgeProfile: %v", err)
	}
	if res.Drift || !res.Unchanged {
		t.Fatalf("force accept must apply drift, got %+v", res)
	}

	stored, err := admin.GetProfile(ctx, profileSpecTestTenant, current.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if stored.Protocol != "onvif" {
		t.Fatalf("blank frame must not clobber operator protocol, got %q", stored.Protocol)
	}
	got := stored.Descriptors[0]
	if got.DataType != "float" || got.ReadWrite != "R" || got.Unit != "L/s" {
		t.Fatalf("blank frame must preserve prior spec, got %+v", got)
	}
	var address map[string]any
	if err := json.Unmarshal(got.Address, &address); err != nil {
		t.Fatalf("address JSON: %v", err)
	}
	if address["service"] != "Media" {
		t.Fatalf("blank {} address must not clobber operator address, got %v", address)
	}

	// A subsequent populated frame still adopts the new non-empty values.
	if _, err := admin.UpsertEdgeProfile(ctx, profileSpecTestTenant, "", EdgeProfileUpsert{
		Name:        "edge-drift-profile",
		Protocol:    "bacnet",
		Payload:     json.RawMessage(`{"v":3}`),
		ContentHash: "h3",
		Force:       true,
		Points: []EdgePoint{{
			Name:      "flow",
			DataType:  "int",
			ReadWrite: "RW",
			Address:   json.RawMessage(`{"DeviceInstance":"7"}`),
		}},
	}); err != nil {
		t.Fatalf("populated UpsertEdgeProfile: %v", err)
	}
	stored2, err := admin.GetProfile(ctx, profileSpecTestTenant, current.ID)
	if err != nil {
		t.Fatalf("GetProfile 2: %v", err)
	}
	if stored2.Protocol != "bacnet" {
		t.Fatalf("populated frame must adopt family, got %q", stored2.Protocol)
	}
	g2 := stored2.Descriptors[0]
	if g2.DataType != "int" || g2.ReadWrite != "RW" {
		t.Fatalf("populated frame must adopt spec, got %+v", g2)
	}
	var addr2 map[string]any
	_ = json.Unmarshal(g2.Address, &addr2)
	if addr2["DeviceInstance"] != "7" {
		t.Fatalf("populated frame must adopt address, got %v", addr2)
	}
}

// TestManualProfileRejectsInvalidSpec verifies the neutral vocabulary is
// enforced on write.
func TestManualProfileRejectsInvalidSpec(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := rbac.ContextWithScope(
		context.Background(), rbac.UnrestrictedScope(profileSpecTestTenant))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	resetProfileSpecFixture(t, ctx, pool)
	t.Cleanup(func() { resetProfileSpecFixture(t, context.Background(), pool) })

	cases := []struct {
		name string
		desc DescriptorDTO
	}{
		{"bad data type", DescriptorDTO{
			MeasurementKey: "p", TransformType: "identity", Precision: -1,
			Scale: 1, DataType: "FLOAT64",
		}},
		{"bad read write", DescriptorDTO{
			MeasurementKey: "p", TransformType: "identity", Precision: -1,
			Scale: 1, ReadWrite: "X",
		}},
		{"address array", DescriptorDTO{
			MeasurementKey: "p", TransformType: "identity", Precision: -1,
			Scale: 1, Address: json.RawMessage(`[1,2]`),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := admin.CreateProfile(ctx, profileSpecTestTenant, ProfileDTO{
				Name:        "invalid-spec-profile",
				Descriptors: []DescriptorDTO{tc.desc},
			})
			if err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}
