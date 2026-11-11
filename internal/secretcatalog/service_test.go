// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go verifies the descriptor service against an in-memory fake
// registry: hello registration (compile-first, partial success), manual
// CRUD, clone-to-manual, descriptor fallback to blank and best-effort
// validation.

package secretcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeRegistry is an in-memory registry implementation.
type fakeRegistry struct {
	rows    map[string]SecretType
	upserts int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{rows: map[string]SecretType{}}
}

func keyOf(tenantID, secretType, version string) string {
	return tenantID + "|" + secretType + "|" + version
}

func (f *fakeRegistry) Upsert(_ context.Context, reg Registration, tenantID, registeredBy string) (SecretType, error) {
	st := SecretType{
		ID:           "st-" + reg.SecretType,
		TenantID:     tenantID,
		SecretType:   reg.SecretType,
		TypeVersion:  reg.TypeVersion,
		FieldsSchema: reg.FieldsSchema,
		DisplayName:  reg.DisplayName,
		Source:       SourceEdge,
		RegisteredBy: registeredBy,
	}
	f.rows[keyOf(tenantID, reg.SecretType, reg.TypeVersion)] = st
	f.upserts++
	return st, nil
}

func (f *fakeRegistry) Create(_ context.Context, reg Registration, tenantID string) (SecretType, error) {
	k := keyOf(tenantID, reg.SecretType, reg.TypeVersion)
	if _, ok := f.rows[k]; ok {
		return SecretType{}, ErrConflict
	}
	st := SecretType{
		ID:           "st-" + reg.SecretType,
		TenantID:     tenantID,
		SecretType:   reg.SecretType,
		TypeVersion:  reg.TypeVersion,
		FieldsSchema: reg.FieldsSchema,
		DisplayName:  reg.DisplayName,
		Source:       SourceManual,
	}
	f.rows[k] = st
	return st, nil
}

func (f *fakeRegistry) Update(_ context.Context, tenantID, secretType, version string, reg Registration) (SecretType, error) {
	k := keyOf(tenantID, secretType, version)
	st, ok := f.rows[k]
	if !ok || st.Source != SourceManual {
		return SecretType{}, ErrNotFound
	}
	st.SecretType = reg.SecretType
	st.TypeVersion = reg.TypeVersion
	st.FieldsSchema = reg.FieldsSchema
	st.DisplayName = reg.DisplayName
	delete(f.rows, k)
	f.rows[keyOf(tenantID, reg.SecretType, reg.TypeVersion)] = st
	return st, nil
}

func (f *fakeRegistry) Delete(_ context.Context, tenantID, secretType, version string) error {
	k := keyOf(tenantID, secretType, version)
	st, ok := f.rows[k]
	if !ok || st.Source != SourceManual {
		return ErrNotFound
	}
	delete(f.rows, k)
	return nil
}

func (f *fakeRegistry) Get(_ context.Context, tenantID, secretType, version string) (SecretType, error) {
	if st, ok := f.rows[keyOf(tenantID, secretType, version)]; ok {
		return st, nil
	}
	return SecretType{}, ErrNotFound
}

func (f *fakeRegistry) List(_ context.Context, tenantID string) ([]SecretType, error) {
	out := make([]SecretType, 0, len(f.rows))
	for _, st := range f.rows {
		if st.TenantID == tenantID {
			out = append(out, st)
		}
	}
	return out, nil
}

const tenantA = "00000000-0000-0000-0000-0000000000a1"
const gatewayA = "0000000-0000-0000-0000-0000000000b1"

// objectSchema is a minimal valid object schema.
var objectSchema = json.RawMessage(`{
  "type":"object",
  "properties":{"username":{"type":"string"},"password":{"type":"string"}},
  "required":["username","password"]
}`)

func TestRegisterFromHello(t *testing.T) {
	svc := New(newFakeRegistry())
	ctx := context.Background()

	results := svc.RegisterFromHello(ctx, tenantA, gatewayA, []Registration{
		{SecretType: "onvif-basic", TypeVersion: "1", FieldsSchema: objectSchema},
		{SecretType: "", TypeVersion: "1", FieldsSchema: objectSchema},
		{SecretType: "bad", TypeVersion: "1",
			FieldsSchema: json.RawMessage(`{"type":"not-a-type"}`)},
	})
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	if !results[0].Accepted {
		t.Fatalf("valid descriptor rejected: %s", results[0].Reason)
	}
	if results[1].Accepted || results[1].Reason == "" {
		t.Fatal("missing type must be rejected with reason")
	}
	if results[2].Accepted {
		t.Fatal("invalid schema must be rejected")
	}
}

func TestManualCRUD(t *testing.T) {
	reg := newFakeRegistry()
	svc := New(reg)
	ctx := context.Background()

	st, err := svc.CreateManual(ctx, tenantA,
		Registration{SecretType: "custom", FieldsSchema: objectSchema})
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != SourceManual {
		t.Fatalf("want source manual, got %s", st.Source)
	}
	if _, err := svc.CreateManual(ctx, tenantA,
		Registration{SecretType: "custom", FieldsSchema: objectSchema}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}

	updated, err := svc.UpdateManual(ctx, tenantA, "custom", "",
		Registration{SecretType: "custom2", FieldsSchema: objectSchema})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SecretType != "custom2" {
		t.Fatalf("want custom2, got %s", updated.SecretType)
	}
	if err := svc.DeleteManual(ctx, tenantA, "custom2", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteManual(ctx, tenantA, "custom2", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestEdgeDescriptorsAreProtected(t *testing.T) {
	reg := newFakeRegistry()
	svc := New(reg)
	ctx := context.Background()

	svc.RegisterFromHello(ctx, tenantA, gatewayA, []Registration{
		{SecretType: "edge-type", FieldsSchema: objectSchema},
	})
	if err := reg.Delete(ctx, tenantA, "edge-type", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("edge descriptors must not be deleted as manual")
	}
}

func TestCloneToManual(t *testing.T) {
	reg := newFakeRegistry()
	svc := New(reg)
	ctx := context.Background()

	svc.RegisterFromHello(ctx, tenantA, gatewayA, []Registration{
		{SecretType: "edge-type", TypeVersion: "1", FieldsSchema: objectSchema,
			DisplayName: map[string]string{"en": "Edge"}},
	})
	st, err := svc.CloneToManual(ctx, tenantA, "edge-type", "1", "edge-copy")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != SourceManual || st.SecretType != "edge-copy" {
		t.Fatalf("unexpected clone: %+v", st)
	}
}

func TestDescriptorFallback(t *testing.T) {
	svc := New(newFakeRegistry())
	ctx := context.Background()

	st, err := svc.Descriptor(ctx, tenantA, "unknown", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != SourceBlank {
		t.Fatalf("want blank descriptor, got %s", st.Source)
	}
	if string(st.FieldsSchema) != string(blankSchema) {
		t.Fatalf("want permissive schema, got %s", st.FieldsSchema)
	}
}

func TestValidate(t *testing.T) {
	svc := New(newFakeRegistry())
	ctx := context.Background()

	// No registered descriptor: any document accepted (blank).
	if err := svc.Validate(ctx, tenantA, "unknown", "",
		json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatalf("blank type must accept document: %v", err)
	}

	svc.RegisterFromHello(ctx, tenantA, gatewayA, []Registration{
		{SecretType: "typed", FieldsSchema: objectSchema},
	})
	if err := svc.Validate(ctx, tenantA, "typed", "",
		json.RawMessage(`{"username":"admin"}`)); !errors.Is(err, ErrFieldsViolation) {
		t.Fatalf("missing required field must violate schema, got %v", err)
	}
	if err := svc.Validate(ctx, tenantA, "typed", "",
		json.RawMessage(`{"username":"admin","password":"secret"}`)); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
}
