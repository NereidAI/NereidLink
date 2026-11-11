// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// mappings_test.go verifies the shared connector entry contract used by both
// profile template validation and connector-instance override validation.

package jsonutil

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateMappingArray(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"absent", "", true},
		{"empty array", "[]", true},
		{"null", "null", true},
		{"valid object with key", `[{"source":"holding:40001","key":"temperature"}]`, true},
		{"multiple valid", `[{"key":"a"},{"register":"40002","key":"b"}]`, true},
		{"not array", `{"key":"a"}`, false},
		{"array of strings", `["temperature"]`, false},
		{"array of numbers", `[1]`, false},
		{"entry not object", `[42]`, false},
		{"missing key", `[{"register":"40001"}]`, false},
		{"empty key", `[{"key":""}]`, false},
		{"non-string key", `[{"key":7}]`, false},
		{"malformed", `[{`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMappingArray(json.RawMessage(tc.raw))
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidShape) {
				t.Fatalf("want ErrInvalidShape, got %v", err)
			}
		})
	}
}

func TestValidateConfigObject(t *testing.T) {
	good := []string{"", "null", "{}", `{"host":"1.2.3.4","port":502}`}
	for _, raw := range good {
		if err := ValidateConfigObject(json.RawMessage(raw)); err != nil {
			t.Fatalf("%q must be accepted: %v", raw, err)
		}
	}
	bad := []string{`[]`, `"x"`, `5`, `{`}
	for _, raw := range bad {
		if err := ValidateConfigObject(json.RawMessage(raw)); !errors.Is(err, ErrInvalidShape) {
			t.Fatalf("%q must be rejected with ErrInvalidShape, got %v", raw, err)
		}
	}
}
