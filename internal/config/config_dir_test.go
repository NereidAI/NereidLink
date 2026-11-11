// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoadDirectoryMergesLayers verifies the ConfigMap-base + Secret-overlay
// layout: files are applied in filename order, nested maps merge deeply,
// and scalars in the later secret layer override the base while the rest
// of its section survives.
func TestLoadDirectoryMergesLayers(t *testing.T) {
	dir := t.TempDir()
	base := `
blob:
  provider: local
  media_max_bytes: 10485760
  media_allowed_types:
    - image/
identity:
  access_ttl: 30m
`
	secret := `
blob:
  media_max_bytes: 20971520
identity:
  jwt_secret: overlay-secret
`
	if err := os.WriteFile(filepath.Join(dir, "10-base.yaml"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "90-secret.yaml"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-YAML sibling must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}

	merged, err := readConfigLayer(dir)
	if err != nil {
		t.Fatalf("readConfigLayer: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged output is not valid YAML: %v", err)
	}
	blob := got["blob"].(map[string]any)
	if blob["provider"] != "local" {
		t.Errorf("provider lost during merge: %v", blob["provider"])
	}
	if v, _ := blob["media_max_bytes"].(int); v != 20971520 {
		t.Errorf("overlay scalar did not win: %v", blob["media_max_bytes"])
	}
	types, ok := blob["media_allowed_types"].([]any)
	if !ok || len(types) != 1 || types[0] != "image/" {
		t.Errorf("base sequence not preserved: %v", blob["media_allowed_types"])
	}
	identity := got["identity"].(map[string]any)
	if identity["jwt_secret"] != "overlay-secret" {
		t.Errorf("overlay key missing: %v", identity)
	}
	if identity["access_ttl"] != "30m" {
		t.Errorf("base sibling key lost: %v", identity)
	}
}

// TestLoadFileUnchanged verifies a plain file path still parses directly.
func TestLoadFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("blob:\n  provider: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readConfigLayer(path)
	if err != nil {
		t.Fatalf("readConfigLayer: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["blob"].(map[string]any)["provider"] != "local" {
		t.Fatalf("unexpected content: %v", got)
	}
}
