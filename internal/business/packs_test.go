// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package business

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// samplePackDir is the committed external template-pack directory relative to
// this package. The binary itself ships no business vocabulary; the packs in
// samples/ are the importable reference content.
var samplePackDir = filepath.Join("..", "..", "samples", "business-templates")

// loadSampleTemplates parses every committed sample pack, sorted by id@version
// for deterministic iteration.
func loadSampleTemplates(t *testing.T) []Template {
	t.Helper()
	entries, err := os.ReadDir(samplePackDir)
	if err != nil {
		t.Fatalf("read sample template packs: %v", err)
	}
	var templates []Template
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(samplePackDir, entry.Name()))
		if err != nil {
			t.Fatalf("read sample pack %s: %v", entry.Name(), err)
		}
		parsed, err := ParseTemplatePack(raw)
		if err != nil {
			t.Fatalf("parse sample pack %s: %v", entry.Name(), err)
		}
		templates = append(templates, parsed...)
	}
	sort.SliceStable(templates, func(i, j int) bool {
		if templates[i].ID != templates[j].ID {
			return templates[i].ID < templates[j].ID
		}
		return templates[i].Version < templates[j].Version
	})
	return templates
}

// loadSampleTemplate loads one exact sample pack version or fails the test.
func loadSampleTemplate(t *testing.T, id, version string) Template {
	t.Helper()
	for _, template := range loadSampleTemplates(t) {
		if template.ID == id && template.Version == version {
			return template
		}
	}
	t.Fatalf("sample template %s/%s not found", id, version)
	return Template{}
}

// sampleTemplateVersion maps a reference template id to its committed pack
// version. New reference packs must bring their own version explicitly.
func sampleTemplateVersion(id string) string {
	if id == "face-access-control" {
		return "0.0.1"
	}
	return "1.0.0"
}
