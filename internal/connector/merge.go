// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// merge.go implements the ADR-0026 two-level connector configuration: a
// device profile supplies a connector TEMPLATE (type/version + default config
// object and point mappings) and a connector instance supplies per-device
// OVERRIDES. At dispatch time NL merges them into the single effective spec
// the edge receives. The JSON contents stay opaque to the kernel: merging
// only understands object/array SHAPE, never individual protocol keys.

package connector

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nereidai/nereidlink/internal/jsonutil"
)

// mergeConfig deep-merges an instance override object on top of a profile
// template object. For an object key present in both, the merge recurses; for
// any other value (string, number, bool, array) the override REPLACES the
// template value wholesale. Arrays are never concatenated: point lists and
// whitelists must be overridden intentionally rather than silently combined.
// A nil/empty override leaves the template unchanged and vice versa.
func mergeConfig(template, override json.RawMessage) json.RawMessage {
	tObj := asObject(template)
	if tObj == nil {
		// No template base: the override is the effective config.
		if len(override) != 0 {
			return compact(override)
		}
		return json.RawMessage("{}")
	}
	oObj := asObject(override)
	if oObj == nil {
		// No usable object override: the template stands.
		return compact(template)
	}
	merged := mergeObjects(tObj, oObj)
	raw, err := json.Marshal(merged)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

// mergeObjects recursively applies override onto base, returning a new map
// (inputs are never mutated). Nested maps merge key by key; any other value
// type in override replaces the base value.
func mergeObjects(base, override map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, ov := range override {
		bv, exists := out[k]
		if !exists {
			out[k] = ov
			continue
		}
		bNested := asObject(bv)
		oNested := asObject(ov)
		if bNested != nil && oNested != nil {
			raw, err := json.Marshal(mergeObjects(bNested, oNested))
			if err != nil {
				out[k] = ov
				continue
			}
			out[k] = raw
			continue
		}
		out[k] = ov
	}
	return out
}

// asObject parses raw as a JSON object; it returns nil for non-objects, null
// or malformed JSON so callers treat those as "no override at this level".
func asObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil
	}
	return obj
}

// nonEmptyArray reports whether raw is a JSON array with at least one
// element; "[]", null and non-array JSON count as empty.
func nonEmptyArray(raw json.RawMessage) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	return len(arr) > 0
}

// mergeMappings resolves the effective point mappings: an instance that
// declares any mappings replaces the template list entirely (arrays are not
// concatenated, mirroring mergeConfig); otherwise the template list is used.
// It always returns a JSON array (empty array rather than null).
func mergeMappings(template, override json.RawMessage) json.RawMessage {
	if nonEmptyArray(override) {
		return compact(override)
	}
	if nonEmptyArray(template) {
		return compact(template)
	}
	return json.RawMessage("[]")
}

// resolveType returns the explicit instance value when present, otherwise the
// template value.
func resolveType(instance, template string) string {
	if instance != "" {
		return instance
	}
	return template
}

// compact re-encodes raw JSON to compact form; nil input yields the empty
// shape so the downlink always carries valid JSON.
func compact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}

// ErrTemplateMissing means an instance references a profile that does not
// carry a connector template.
var ErrTemplateMissing = errors.New("referenced profile has no connector template")

// effectiveSpec applies the profile template to a stored instance to produce
// the single spec dispatched to the edge. The stored row keeps the raw
// per-instance overrides plus the profile reference; the merge is computed at
// push/redrive time so a later edit to the profile template flows to the edge
// on the next desired push without rewriting every instance.
//
// tpl is nil for an instance with no profile: its stored config/mappings are
// sent (normalized to an object/array). An instance that references a profile
// whose connector_type is empty returns ErrTemplateMissing.
func effectiveSpec(sp Spec, tpl *Template) (Spec, error) {
	out := sp
	if tpl == nil {
		out.Config = mergeConfig(nil, sp.Config)
		out.Mappings = mergeMappings(nil, sp.Mappings)
		return out, nil
	}
	if tpl.ConnectorType == "" {
		return Spec{}, ErrTemplateMissing
	}
	out.ConnectorType = resolveType(sp.ConnectorType, tpl.ConnectorType)
	out.TypeVersion = resolveType(sp.TypeVersion, tpl.TypeVersion)
	out.Config = mergeConfig(tpl.Config, sp.Config)
	out.Mappings = mergeMappings(tpl.Mappings, sp.Mappings)
	return out, nil
}

// validateOverrideShape rejects override JSON whose top-level shape is
// incompatible with the merge: config must be an object and mappings an array
// of point-mapping objects (each with a string "key"). Empty or null input is
// allowed and means "inherit the template".
func validateOverrideShape(in SaveInput) error {
	if err := jsonutil.ValidateConfigObject(nullAsEmpty(in.Config)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := jsonutil.ValidateMappingArray(nullAsEmpty(in.Mappings)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// nullAsEmpty maps a literal JSON null to an absent document so optional
// override fields accept null as "unset"; every other document is untouched.
func nullAsEmpty(raw json.RawMessage) json.RawMessage {
	if string(raw) == "null" {
		return nil
	}
	return raw
}
