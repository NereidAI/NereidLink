// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secretfields.go inspects a descriptor schema and reports which document
// paths hold secret material (JSON Schema `format: password` or the extension
// `x-secret: true`). The delivery path uses it to replace those values with
// secret references before a frame leaves the core; the console uses it to
// pick password controls. Paths are dot-separated and traverse nested
// object properties.

package secretcatalog

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrEmptyType means secret paths were requested without a type key.
var ErrEmptyType = errors.New("edge secret type is required")

// SecretPaths returns the secret document paths for the registered type.
// Unregistered (blank) types report no paths: the operator marks secret
// fields per row in the ad-hoc editor.
func (s *Service) SecretPaths(ctx context.Context, tenantID, secretType, typeVersion string) ([]string, error) {
	if secretType == "" {
		return []string{}, nil
	}
	st, err := s.store.Get(ctx, tenantID, secretType, typeVersion)
	if errors.Is(err, ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(st.FieldsSchema, &schema); err != nil {
		return nil, err
	}
	paths := []string{}
	collectSecretPaths(schema, "", &paths)
	return paths, nil
}

// collectSecretPaths walks schema's properties recursively.
func collectSecretPaths(schema map[string]any, prefix string, out *[]string) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	for key, raw := range properties {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if isSecretProperty(prop) {
			*out = append(*out, path)
		}
		collectSecretPaths(prop, path, out)
	}
}

// isSecretProperty reports the two markers the catalog recognizes.
func isSecretProperty(prop map[string]any) bool {
	if prop["format"] == "password" {
		return true
	}
	secret, ok := prop["x-secret"].(bool)
	return ok && secret
}
