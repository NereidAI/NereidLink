// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validator_test.go covers schema compilation/caching and the pure
// compile+validate behaviour without a database.
package connectorcatalog

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCompileRejectsInvalidSchema(t *testing.T) {
	if _, err := compile(json.RawMessage(`{"type":"not-a-type"}`)); err == nil || !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("unknown type must be ErrInvalidSchema, got %v", err)
	}
	if _, err := compile(json.RawMessage(`{not json`)); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("malformed schema must be ErrInvalidSchema, got %v", err)
	}
	if _, err := compile(nil); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("empty schema must be ErrInvalidSchema, got %v", err)
	}
}

func TestCompileAndValidate(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"host":{"type":"string"},"port":{"type":"integer","minimum":1,"maximum":65535}
		},
		"required":["host"],"additionalProperties":false
	}`)
	sch, err := compile(schema)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	good := map[string]any{"host": "10.0.0.1", "port": 502}
	if err := sch.Validate(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, bad := range map[string]any{
		"missing required": map[string]any{"port": 502},
		"wrong type":       map[string]any{"host": 7},
		"out of range":     map[string]any{"host": "h", "port": 70000},
		"additional prop":  map[string]any{"host": "h", "bogus": 1},
	} {
		if err := sch.Validate(bad); err == nil {
			t.Errorf("%s must fail validation", name)
		}
	}
}

func TestCacheReusesCompiled(t *testing.T) {
	c := newCache()
	raw := json.RawMessage(`{"type":"object"}`)
	a, err := c.compiled(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.compiled(raw)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("identical document must reuse the cached schema")
	}
	if _, err := c.compiled(json.RawMessage(`{"type":1}`)); err == nil {
		t.Fatal("invalid schema via cache must error")
	}
}
