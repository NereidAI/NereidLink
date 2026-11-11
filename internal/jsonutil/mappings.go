// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package jsonutil holds transport-independent JSON helpers shared by the
// registry (profile connector templates) and the connector service
// (per-instance overrides). The platform never interprets connector-protocol
// contents, but both write paths share the same minimal entry contract.
package jsonutil

import (
	"encoding/json"
	"fmt"
)

// ErrInvalidShape marks a JSON document whose shape violates a shared
// connector entry contract.
var ErrInvalidShape = fmt.Errorf("invalid JSON shape")

// ValidateMappingArray enforces the common point-mappings entry contract:
// mappings is a JSON array of objects, each carrying a non-empty string
// "key" identifying the platform measurement key the connector source maps
// onto. An empty, null or literal "[]" document is valid and means "no
// mappings" (for an instance override it also means "inherit the template").
//
// The other properties of each entry stay opaque and connector-specific
// (register/source/...); the platform neither reads nor constrains them.
func ValidateMappingArray(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("%w: mappings must be a JSON array of objects", ErrInvalidShape)
	}
	for i, entry := range arr {
		if entry == nil {
			return fmt.Errorf("%w: mappings[%d] must be a JSON object", ErrInvalidShape, i)
		}
		value, ok := entry["key"]
		if !ok {
			return fmt.Errorf("%w: mappings[%d] is missing \"key\"", ErrInvalidShape, i)
		}
		var key string
		if err := json.Unmarshal(value, &key); err != nil {
			return fmt.Errorf("%w: mappings[%d].key must be a string", ErrInvalidShape, i)
		}
		if key == "" {
			return fmt.Errorf("%w: mappings[%d].key must not be empty", ErrInvalidShape, i)
		}
	}
	return nil
}

// ValidateConfigObject enforces that a connector config document is a JSON
// object. An empty/null document is allowed and means "absent" (for an
// instance override it means "inherit the template").
func ValidateConfigObject(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return fmt.Errorf("%w: config must be a JSON object", ErrInvalidShape)
	}
	return nil
}
