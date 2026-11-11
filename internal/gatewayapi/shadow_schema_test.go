// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_schema_test.go verifies writable profile descriptors project to the
// desired-state editor catalog with the right types, ranges and defaults.
package gatewayapi

import (
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

func ptr(v float64) *float64 { return &v }

func TestDesiredFieldsFromProfile(t *testing.T) {
	profile := registry.ProfileDTO{
		ID: "p1",
		Descriptors: []registry.DescriptorDTO{
			{MeasurementKey: "rw-number", DataType: "int", ReadWrite: "RW",
				EdgeMin: ptr(0), EdgeMax: ptr(100), EdgeDefault: "42", Unit: "rpm"},
			{MeasurementKey: "w-bool", DataType: "bool", ReadWrite: "W",
				EdgeDefault: "true"},
			{MeasurementKey: "w-string", DataType: "string", ReadWrite: "W",
				EdgeDefault: "AUTO"},
			{MeasurementKey: "ro-point", DataType: "float", ReadWrite: "R"},
			{MeasurementKey: "unknown-rw", DataType: "", ReadWrite: "RW"},
			{MeasurementKey: "binary-rw", DataType: "binary", ReadWrite: "RW"},
			{MeasurementKey: "inactive-w", DataType: "int", ReadWrite: "W",
				Inactive: true},
			{MeasurementKey: "", ReadWrite: "W"},
			{MeasurementKey: "valid-range-fallback", DataType: "float",
				ReadWrite: "RW", ValidMin: ptr(1), ValidMax: ptr(9)},
		},
	}

	fields := desiredFieldsFromProfile(profile)
	byKey := map[string]DesiredKeyField{}
	for _, f := range fields {
		byKey[f.Key] = f
	}

	if len(fields) != 6 {
		t.Fatalf("expected 6 writable fields (RW/W active), got %d: %+v", len(fields), fields)
	}

	n := byKey["rw-number"]
	if n.Type != "number" || n.Unit != "rpm" || n.Min == nil || *n.Min != 0 ||
		n.Max == nil || *n.Max != 100 || n.Default != float64(42) {
		t.Fatalf("number field wrong: %+v", n)
	}

	b := byKey["w-bool"]
	if b.Type != "boolean" || b.Default != true {
		t.Fatalf("boolean field wrong: %+v", b)
	}

	s := byKey["w-string"]
	if s.Type != "string" || s.Default != "AUTO" {
		t.Fatalf("string field wrong: %+v", s)
	}

	if u := byKey["unknown-rw"]; u.Type != "string" {
		t.Fatalf("unknown data type must default to string: %+v", u)
	}
	if bin := byKey["binary-rw"]; bin.Type != "string" {
		t.Fatalf("binary must map to string editor: %+v", bin)
	}

	v := byKey["valid-range-fallback"]
	if v.Min == nil || *v.Min != 1 || v.Max == nil || *v.Max != 9 {
		t.Fatalf("valid_min/valid_max fallback wrong: %+v", v)
	}
}

func TestDesiredFieldsFromProfileEmpty(t *testing.T) {
	fields := desiredFieldsFromProfile(registry.ProfileDTO{})
	if fields == nil || len(fields) != 0 {
		t.Fatalf("no descriptors must yield a non-nil empty slice, got %+v", fields)
	}
}

func TestParseDefault(t *testing.T) {
	if v := parseDefault("number", "3.5"); v != float64(3.5) {
		t.Fatalf("number default: %v", v)
	}
	if v := parseDefault("boolean", "false"); v != false {
		t.Fatalf("boolean default: %v", v)
	}
	if v := parseDefault("string", `"q"`); v != "q" {
		t.Fatalf("json string default: %v", v)
	}
	if v := parseDefault("string", "raw-text"); v != "raw-text" {
		t.Fatalf("plain text default: %v", v)
	}
	if v := parseDefault("number", ""); v != nil {
		t.Fatalf("empty default must be nil: %v", v)
	}
	if v := parseDefault("number", "not-a-number"); v != nil {
		t.Fatalf("unparseable number must be nil: %v", v)
	}
}
