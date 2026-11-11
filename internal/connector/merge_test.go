// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// merge_test.go verifies the ADR-0026 two-level connector configuration
// merge: a profile template supplies defaults and an instance supplies
// per-device overrides, merged at dispatch time. The kernel never interprets
// individual protocol keys, so the tests assert only object/array shape
// semantics and precedence.

package connector

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

func TestMergeConfigOverrideWins(t *testing.T) {
	tpl := json.RawMessage(`{"host":"10.0.0.1","unit_id":1,"tls":{"ca":"a","verify":false}}`)
	ov := json.RawMessage(`{"host":"10.0.0.2","tls":{"verify":true}}`)
	m := decode(t, mergeConfig(tpl, ov))
	if m["host"] != "10.0.0.2" {
		t.Fatalf("override host must win, got %v", m["host"])
	}
	if m["unit_id"].(float64) != 1 {
		t.Fatalf("template-only key must be preserved, got %v", m["unit_id"])
	}
	tls := m["tls"].(map[string]any)
	if tls["ca"] != "a" {
		t.Fatalf("nested template key must be preserved, got %v", tls)
	}
	if tls["verify"] != true {
		t.Fatalf("nested override must win, got %v", tls)
	}
}

func TestMergeConfigArrayReplacesWhole(t *testing.T) {
	tpl := json.RawMessage(`{"options":["a","b"]}`)
	ov := json.RawMessage(`{"options":["x"]}`)
	m := decode(t, mergeConfig(tpl, ov))
	opts := m["options"].([]any)
	if len(opts) != 1 || opts[0] != "x" {
		t.Fatalf("array override must replace, not concatenate: %v", opts)
	}
}

func TestMergeConfigEmptySides(t *testing.T) {
	if string(mergeConfig(nil, json.RawMessage(`{"a":1}`))) != `{"a":1}` {
		t.Fatal("override-only must be the effective config")
	}
	m := decode(t, mergeConfig(json.RawMessage(`{"a":1}`), nil))
	if m["a"].(float64) != 1 {
		t.Fatal("template-only must stand when override is empty")
	}
	if string(mergeConfig(nil, nil)) != "{}" {
		t.Fatal("no config must normalize to an object")
	}
}

func TestMergeMappings(t *testing.T) {
	if string(mergeMappings(nil, json.RawMessage(`[{"k":"v"}]`))) != `[{"k":"v"}]` {
		t.Fatal("instance mappings must win")
	}
	if string(mergeMappings(json.RawMessage(`[{"k":"t"}]`), json.RawMessage(`[]`))) != `[{"k":"t"}]` {
		t.Fatal("empty instance mappings must fall back to template")
	}
	if string(mergeMappings(nil, nil)) != "[]" {
		t.Fatal("no mappings must normalize to an array")
	}
}

func TestEffectiveSpecResolvesTypeAndMerges(t *testing.T) {
	sp := Spec{
		ID:            "s1",
		ConnectorType: "", // inherited from template
		TypeVersion:   "2.1",
		Config:        json.RawMessage(`{"host":"h2"}`),
	}
	tpl := &Template{
		ProfileID:     "p1",
		ConnectorType: "modbus-tcp",
		TypeVersion:   "1.0",
		Config:        json.RawMessage(`{"host":"h1","port":502}`),
		Mappings:      json.RawMessage(`[{"register":"40001"}]`),
	}
	eff, err := effectiveSpec(sp, tpl)
	if err != nil {
		t.Fatal(err)
	}
	if eff.ConnectorType != "modbus-tcp" {
		t.Fatalf("type must inherit from template, got %q", eff.ConnectorType)
	}
	if eff.TypeVersion != "2.1" {
		t.Fatalf("explicit version must win, got %q", eff.TypeVersion)
	}
	m := decode(t, eff.Config)
	if m["host"] != "h2" || m["port"].(float64) != 502 {
		t.Fatalf("merged config wrong: %v", m)
	}
	if string(eff.Mappings) != `[{"register":"40001"}]` {
		t.Fatalf("template mappings should be inherited: %s", eff.Mappings)
	}
}

func TestEffectiveSpecTemplateWithoutConnectorType(t *testing.T) {
	if _, err := effectiveSpec(Spec{ProfileID: "p1"}, &Template{ProfileID: "p1"}); err != ErrTemplateMissing {
		t.Fatalf("want ErrTemplateMissing, got %v", err)
	}
}

func TestEffectiveSpecNoTemplateNormalizes(t *testing.T) {
	eff, err := effectiveSpec(Spec{ConnectorType: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(eff.Config) != "{}" || string(eff.Mappings) != "[]" {
		t.Fatalf("unbound spec must normalize JSON shapes: %s %s", eff.Config, eff.Mappings)
	}
}

func TestValidateOverrideShape(t *testing.T) {
	if err := validateOverrideShape(SaveInput{
		Config:   json.RawMessage(`{"a":1}`),
		Mappings: json.RawMessage(`[{"x":1,"key":"temperature"}]`),
	}); err != nil {
		t.Fatalf("valid shapes must pass: %v", err)
	}
	if err := validateOverrideShape(SaveInput{Config: json.RawMessage(`[1,2]`)}); err == nil {
		t.Fatal("non-object config must be rejected")
	}
	if err := validateOverrideShape(SaveInput{Mappings: json.RawMessage(`{"a":1}`)}); err == nil {
		t.Fatal("non-array mappings must be rejected")
	}
	if err := validateOverrideShape(SaveInput{Mappings: json.RawMessage(`[{"x":1}]`)}); err == nil {
		t.Fatal("mapping entry without a key must be rejected")
	}
	if err := validateOverrideShape(SaveInput{Config: json.RawMessage(`null`), Mappings: json.RawMessage(`null`)}); err != nil {
		t.Fatalf("null overrides mean inherit and must pass: %v", err)
	}
	if err := validateOverrideShape(SaveInput{Mappings: json.RawMessage(`[]`)}); err != nil {
		t.Fatalf("empty mappings array must pass: %v", err)
	}
}
