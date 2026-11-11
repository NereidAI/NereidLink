// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// apidocs_test.go guards the embedded OpenAPI contract against the kind of
// defect Swagger UI rejects at load time: yaml.v3 (unlike the more lenient
// PyYAML used in ad-hoc checks) errors on duplicate mapping keys, so this
// test fails the build whenever a key is accidentally repeated.
package apidocs

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSpecIsValidOpenAPI verifies the embedded document parses without
// duplicate-key errors and declares a supported OpenAPI version.
func TestSpecIsValidOpenAPI(t *testing.T) {
	raw, err := Spec()
	if err != nil {
		t.Fatalf("read embedded spec: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("embedded spec is not valid YAML (check duplicate keys): %v", err)
	}

	version, _ := doc["openapi"].(string)
	if !strings.HasPrefix(version, "3.") {
		t.Fatalf("openapi version = %q, want 3.x.y", version)
	}
	if _, ok := doc["paths"]; !ok {
		t.Fatal("spec has no paths section")
	}
}
