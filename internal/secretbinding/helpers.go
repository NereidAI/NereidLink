// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// helpers.go contains small document manipulation utilities for building
// delivery frames.

package secretbinding

import (
	"encoding/json"
	"strings"

	"github.com/nereidai/nereidlink/internal/secrets"
)

// ParseFieldReference splits a "secret://<name>#<field-path>" reference
// used inside a delivery document. It returns ok=false for plain
// secret://<name> references (connector style) or malformed strings, so
// the last-mile relay leaves non-field values untouched.
func ParseFieldReference(raw string) (name, fieldPath string, ok bool) {
	if !strings.HasPrefix(raw, secrets.Scheme) {
		return "", "", false
	}
	body := strings.TrimPrefix(raw, secrets.Scheme)
	separator := strings.IndexByte(body, '#')
	if separator <= 0 || separator == len(body)-1 {
		return "", "", false
	}
	name = body[:separator]
	fieldPath = body[separator+1:]
	if !validName(name) {
		return "", "", false
	}
	return name, fieldPath, true
}

// validName mirrors the tenant_secret name CHECK via the reference parser.
func validName(name string) bool {
	_, ok := secrets.ParseReference(secrets.Scheme + name)
	return ok
}

// ResolveEntryFields walks a delivery entry document depth-first and
// substitutes every secret://name#field reference with the plaintext value
// from documents. Unresolvable references are returned in missing and left
// in place. It is the single last-mile substitution used by the MQTT relay.
func ResolveEntryFields(doc map[string]any, documents map[string]map[string]any) []string {
	missing := map[string]struct{}{}
	resolveValueFields(doc, documents, missing)
	out := make([]string, 0, len(missing))
	for name := range missing {
		out = append(out, name)
	}
	return out
}

// resolveValueFields recursively substitutes references in map and slice
// nodes.
func resolveValueFields(value any, documents map[string]map[string]any, missing map[string]struct{}) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if raw, ok := child.(string); ok {
				if name, path, ok := ParseFieldReference(raw); ok {
					source, exists := documents[name]
					if !exists {
						missing[name] = struct{}{}
						continue
					}
					plain, found := valueAtPath(source, path)
					if !found {
						missing[name+"#"+path] = struct{}{}
						continue
					}
					node[key] = plain
					continue
				}
			}
			resolveValueFields(child, documents, missing)
		}
	case []any:
		for _, child := range node {
			resolveValueFields(child, documents, missing)
		}
	}
}

// jsonMarshal is the standard JSON encoder.
func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

// cloneMap shallow-copies a document before mutation.
func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// valueAtPath reads a dot-separated path.
func valueAtPath(doc map[string]any, path string) (any, bool) {
	current := doc
	parts := splitPath(path)
	for i, part := range parts {
		value, ok := current[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return value, true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return nil, false
}

// setPath writes a value at a dot-separated path.
func setPath(doc map[string]any, path string, value any) {
	current := doc
	parts := splitPath(path)
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
}

// splitPath splits on dots.
func splitPath(path string) []string {
	parts := []string{}
	start := 0
	for i, char := range path {
		if char == '.' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	parts = append(parts, path[start:])
	return parts
}
