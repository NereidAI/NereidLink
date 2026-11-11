// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package business

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func readSamplePack(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(samplePackDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseTemplatePackAcceptsSingleEnvelopeAndJSON(t *testing.T) {
	single := readSamplePack(t, "face-access-control-0.0.1.yaml")
	templates, err := ParseTemplatePack(single)
	if err != nil {
		t.Fatalf("parse single-template pack: %v", err)
	}
	if len(templates) != 1 || templates[0].ID != "face-access-control" || templates[0].Version != "0.0.1" {
		t.Fatalf("unexpected single parse: %+v", templates)
	}

	envelope, err := yaml.Marshal(TemplatePack{Templates: loadSampleTemplates(t)})
	if err != nil {
		t.Fatal(err)
	}
	packed, err := ParseTemplatePack(envelope)
	if err != nil || len(packed) != 2 {
		t.Fatalf("parse envelope: %v templates=%d", err, len(packed))
	}

	template := loadSampleTemplate(t, "meter-reading", "1.0.0")
	jsonSingle, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := ParseTemplatePack(jsonSingle)
	if err != nil {
		t.Fatalf("parse json document: %v", err)
	}
	if fromJSON[0].ID != "meter-reading" {
		t.Fatalf("json parse returned %+v", fromJSON[0])
	}
}

func TestParseTemplatePackRejectsEmptyAndMalformed(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("  "), []byte("id: [unterminated")} {
		if _, err := ParseTemplatePack(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("raw=%q expected ErrInvalid, got %v", string(raw), err)
		}
	}
}

func TestImportTemplatesValidatesAndPersists(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	raw := readSamplePack(t, "face-access-control-0.0.1.yaml")

	imported, err := service.ImportTemplates(context.Background(), "tenant", "tester", raw)
	if err != nil {
		t.Fatalf("import sample pack: %v", err)
	}
	if len(imported) != 1 || imported[0].ID != "face-access-control" || imported[0].CreatedAt == nil {
		t.Fatalf("unexpected import result: %+v", imported)
	}
	listed, err := service.ListTemplates(context.Background(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "face-access-control" {
		t.Fatalf("imported template not listed: %+v", listed)
	}
	if _, err := service.ImportTemplates(context.Background(), "tenant", "tester", raw); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-import expected ErrConflict, got %v", err)
	}
}

func TestImportTemplatesRejectsInPackDuplicatesAndInvalidContent(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	valid := loadSampleTemplate(t, "meter-reading", "1.0.0")

	duplicate, err := yaml.Marshal(TemplatePack{Templates: []Template{valid, valid}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportTemplates(context.Background(), "tenant", "tester", duplicate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-pack duplicate expected ErrInvalid, got %v", err)
	}

	bad := valid
	bad.ID = "scripted"
	bad.Assembly.Nodes = append(bad.Assembly.Nodes, Node{
		ID: "rule", Kind: NodeKindRule, Name: "Rule",
		Asset: map[string]any{"script": "return true"},
	})
	raw, err := yaml.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportTemplates(context.Background(), "tenant", "tester", raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("executable content expected ErrInvalid, got %v", err)
	}
	if listed, _ := service.ListTemplates(context.Background(), "tenant"); len(listed) != 0 {
		t.Fatalf("failed import must not persist partial templates: %+v", listed)
	}
}

func TestExportTemplatesRoundTripsThroughParse(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, Owners{}, nil)
	if _, err := service.ImportTemplates(context.Background(), "tenant", "tester",
		readSamplePack(t, "meter-reading-1.0.0.yaml")); err != nil {
		t.Fatal(err)
	}
	pack, err := service.ExportTemplates(context.Background(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Templates) != 1 || pack.Templates[0].ID != "meter-reading" {
		t.Fatalf("unexpected export: %+v", pack.Templates)
	}
	raw, err := yaml.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseTemplatePack(raw)
	if err != nil {
		t.Fatalf("re-parse exported pack: %v", err)
	}
	if reparsed[0].ID != pack.Templates[0].ID || reparsed[0].Version != pack.Templates[0].Version {
		t.Fatalf("export round trip changed identity: %+v", reparsed[0])
	}
}
