// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers hello registration (compile-before-store, partial
// success) and best-effort config validation without a database.
package connectorcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type fakeRegistry struct {
	schemas map[string]Schema // key type|version
	upserts []Registration
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{schemas: map[string]Schema{}}
}

func keyOf(t, v string) string { return t + "|" + v }

func (f *fakeRegistry) Upsert(_ context.Context, reg Registration, tenant, by, source string) (Schema, error) {
	f.upserts = append(f.upserts, reg)
	f.schemas[keyOf(reg.ConnectorType, reg.ConnectorTypeVersion)] = Schema{
		TenantID: tenant, ConnectorType: reg.ConnectorType,
		ConnectorTypeVersion: reg.ConnectorTypeVersion, ConfigSchema: reg.ConfigSchema,
	}
	return f.schemas[keyOf(reg.ConnectorType, reg.ConnectorTypeVersion)], nil
}

func (f *fakeRegistry) Get(_ context.Context, _, connectorType, version string) (Schema, error) {
	if sch, ok := f.schemas[keyOf(connectorType, version)]; ok {
		return sch, nil
	}
	if version != "" {
		if sch, ok := f.schemas[keyOf(connectorType, "")]; ok {
			return sch, nil
		}
	}
	return Schema{}, ErrNotFound
}

func (f *fakeRegistry) HighestVersion(_ context.Context, _, connectorType string) (string, error) {
	best := ""
	for k := range f.schemas {
		t, v, _ := strings.Cut(k, "|")
		if t != connectorType || v == "" {
			continue
		}
		if best == "" || CompareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", ErrNotFound
	}
	return best, nil
}

func testService(reg registry) *Service {
	return New(reg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

// List returns the fake registry's schemas for one tenant. The fake keys by
// type|version without tenant, so tests use a single tenant; TenantID is
// stamped from the request to match the real row shape.
func (f *fakeRegistry) List(_ context.Context, tenant string) ([]Schema, error) {
	out := make([]Schema, 0, len(f.schemas))
	for _, sch := range f.schemas {
		sch.TenantID = tenant
		out = append(out, sch)
	}
	return out, nil
}

// Family returns the first non-empty family registered for the connector
// type, mirroring the fail-open PostgreSQL Store behaviour.
func (f *fakeRegistry) Family(_ context.Context, _, connectorType string) (string, error) {
	for _, sch := range f.schemas {
		if sch.ConnectorType == connectorType && sch.Protocol != "" {
			return sch.Protocol, nil
		}
	}
	return "", nil
}

func TestRegisterFromHelloPartialSuccess(t *testing.T) {
	reg := newFakeRegistry()
	s := testService(reg)
	results := s.RegisterFromHello(context.Background(), "t1", "g1", []Registration{
		{ConnectorType: "modbus", ConnectorTypeVersion: "1",
			ConfigSchema: json.RawMessage(`{"type":"object","required":["host"]}`)},
		{ConnectorType: "bad", ConfigSchema: json.RawMessage(`{"type":123}`)},
		{ConnectorType: "", ConfigSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	if !results[0].Accepted {
		t.Fatalf("valid schema must be accepted: %+v", results[0])
	}
	if len(reg.upserts) != 1 {
		t.Fatalf("only the one valid schema must be stored, got %d", len(reg.upserts))
	}
	if results[1].Accepted || results[1].Reason == "" {
		t.Fatalf("malformed schema must be rejected with a reason: %+v", results[1])
	}
	if results[2].Accepted || results[2].Reason == "" {
		t.Fatalf("type-less entry must be rejected: %+v", results[2])
	}
}

func TestValidateConfigBestEffort(t *testing.T) {
	reg := newFakeRegistry()
	s := testService(reg)
	ctx := context.Background()
	// No schema registered -> accepted (best-effort).
	if err := s.ValidateConfig(ctx, "t1", "unknown", "1", json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatalf("unregistered type must pass, got %v", err)
	}
	s.RegisterFromHello(ctx, "t1", "g1", []Registration{
		{ConnectorType: "modbus", ConnectorTypeVersion: "2",
			ConfigSchema: json.RawMessage(`{"type":"object","required":["host"],"additionalProperties":false,
				"properties":{"host":{"type":"string"}}}`)},
	})
	if err := s.ValidateConfig(ctx, "t1", "modbus", "2", json.RawMessage(`{"host":"10.0.0.1"}`)); err != nil {
		t.Fatalf("valid config must pass, got %v", err)
	}
	err := s.ValidateConfig(ctx, "t1", "modbus", "2", json.RawMessage(`{"port":502}`))
	if !errors.Is(err, ErrConfigViolation) {
		t.Fatalf("violating config must be ErrConfigViolation, got %v", err)
	}
	// Empty type short-circuits to accepted.
	if err := s.ValidateConfig(ctx, "t1", "", "", nil); err != nil {
		t.Fatalf("empty type must pass, got %v", err)
	}
}

func TestValidateFallsBackToUnversioned(t *testing.T) {
	reg := newFakeRegistry()
	s := testService(reg)
	s.RegisterFromHello(context.Background(), "t1", "g1", []Registration{
		{ConnectorType: "modbus",
			ConfigSchema: json.RawMessage(`{"type":"object","required":["host"]}`)},
	})
	if err := s.ValidateConfig(context.Background(), "t1", "modbus", "9.9",
		json.RawMessage(`{"host":"h"}`)); err != nil {
		t.Fatalf("must fall back to unversioned schema, got %v", err)
	}
}

func TestValidateOverrideIsPatchRelaxed(t *testing.T) {
	reg := newFakeRegistry()
	s := testService(reg)
	schema := json.RawMessage(`{
		"type":"object",
		"required":["host"],
		"additionalProperties":false,
		"properties":{
			"host":{"type":"string"},
			"tls":{"type":"object","required":["ca"],"properties":{"ca":{"type":"string"}}},
			"ports":{"type":"array","items":{"type":"integer"}}
		}}`)
	s.RegisterFromHello(context.Background(), "t1", "g1", []Registration{
		{ConnectorType: "modbus", ConfigSchema: schema}})

	// A full config missing "required" fails ValidateConfig...
	if err := s.ValidateConfig(context.Background(), "t1", "modbus", "",
		json.RawMessage(`{"ports":[502]}`)); err == nil {
		t.Fatal("full config without required host must fail ValidateConfig")
	}
	// ...but the same partial document is a valid override (template supplies host).
	if err := s.ValidateOverride(context.Background(), "t1", "modbus", "",
		json.RawMessage(`{"ports":[502]}`)); err != nil {
		t.Fatalf("partial override must pass relaxed validation, got %v", err)
	}
	// Nested required is relaxed too: overriding tls without its required ca.
	if err := s.ValidateOverride(context.Background(), "t1", "modbus", "",
		json.RawMessage(`{"tls":{}}`)); err != nil {
		t.Fatalf("nested partial override must pass, got %v", err)
	}
	// Type violations on keys the override DOES set are still caught.
	if err := s.ValidateOverride(context.Background(), "t1", "modbus", "",
		json.RawMessage(`{"host":7}`)); err == nil {
		t.Fatal("wrong-type override value must still fail")
	}
	// Unknown property with additionalProperties:false is still rejected.
	if err := s.ValidateOverride(context.Background(), "t1", "modbus", "",
		json.RawMessage(`{"bogus":1}`)); err == nil {
		t.Fatal("unknown override property must still fail")
	}
}
