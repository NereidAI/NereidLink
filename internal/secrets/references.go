// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// references.go recognizes and resolves secret:// references embedded in
// connector config JSON. Resolution is the only place plaintext is produced:
// the persisted spec keeps the reference, and the MQTT relay substitutes
// values just before writing the gateway frame.

package secrets

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Scheme is the reference prefix that marks a config string as a secret.
const Scheme = "secret://"

// namePattern mirrors the tenant_secret CHECK constraint so an unresolvable
// reference is indistinguishable (to the resolver) from a name that could
// never have been stored.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// ParseReference returns the secret name when raw is exactly
// "secret://<valid-name>". A string that merely starts with the prefix but
// carries an invalid name is NOT treated as a reference, so it is passed
// through verbatim rather than silently blanked.
func ParseReference(raw string) (string, bool) {
	if !strings.HasPrefix(raw, Scheme) {
		return "", false
	}
	name := strings.TrimPrefix(raw, Scheme)
	if !namePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

// ResolveDocument walks a JSON document and replaces every string that is a
// secret:// reference with its value from values. References missing from the
// map resolve to an empty string and are reported in missing; the caller logs
// them once per frame. Non-string JSON and strings that are not references are
// returned unchanged.
func ResolveDocument(raw json.RawMessage, values map[string]string) (json.RawMessage, []string) {
	if len(raw) == 0 {
		return raw, nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, nil
	}
	missing := map[string]struct{}{}
	resolved := resolveValue(doc, values, missing)
	out, err := json.Marshal(resolved)
	if err != nil {
		return raw, nil
	}
	miss := make([]string, 0, len(missing))
	for name := range missing {
		miss = append(miss, name)
	}
	return out, miss
}

// FindReferences returns the distinct, de-duplicated secret names referenced
// anywhere in raw (any nesting depth). It lets the relay batch-load only the
// secrets a frame actually uses.
func FindReferences(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	collectReferences(doc, seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// collectReferences walks the document recording reference names.
func collectReferences(node any, seen map[string]struct{}) {
	switch v := node.(type) {
	case string:
		if name, ok := ParseReference(v); ok {
			seen[name] = struct{}{}
		}
	case map[string]any:
		for _, child := range v {
			collectReferences(child, seen)
		}
	case []any:
		for _, child := range v {
			collectReferences(child, seen)
		}
	}
}

// resolveValue recursively substitutes references in arbitrary JSON.
func resolveValue(node any, values map[string]string, missing map[string]struct{}) any {
	switch v := node.(type) {
	case string:
		if name, ok := ParseReference(v); ok {
			if val, found := values[name]; found {
				return val
			}
			missing[name] = struct{}{}
			return ""
		}
		return v
	case map[string]any:
		for k, child := range v {
			v[k] = resolveValue(child, values, missing)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = resolveValue(child, values, missing)
		}
		return v
	default:
		return node
	}
}
