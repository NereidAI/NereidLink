// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// packs.go implements import and export of versioned business template packs.
// Packs are YAML or JSON data artifacts: the binary ships no business
// vocabulary of its own, and new assemblies only arrive through this surface
// or the tenant-scoped designer.

package business

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// TemplatePack is the multi-template import/export envelope. A bare
// single-template document is also accepted on import.
type TemplatePack struct {
	// Templates carries the versioned template documents in pack order.
	Templates []Template `json:"templates" yaml:"templates"`
}

// ParseTemplatePack decodes a YAML or JSON pack. A pack may be either the
// TemplatePack envelope or a bare single Template document.
func ParseTemplatePack(raw []byte) ([]Template, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty template pack", ErrInvalid)
	}
	var probe map[string]any
	if err := yaml.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("%w: decode template pack: %v", ErrInvalid, err)
	}
	if _, envelope := probe["templates"]; envelope {
		var pack TemplatePack
		if err := yaml.Unmarshal(trimmed, &pack); err != nil {
			return nil, fmt.Errorf("%w: decode template pack: %v", ErrInvalid, err)
		}
		return pack.Templates, nil
	}
	var template Template
	if err := yaml.Unmarshal(trimmed, &template); err != nil {
		return nil, fmt.Errorf("%w: decode template document: %v", ErrInvalid, err)
	}
	return []Template{template}, nil
}

// ImportTemplates validates and persists every template in a pack as one
// all-or-nothing operation. A duplicate id@version, inside the pack or against
// an existing tenant template, fails the whole import before any row is
// written.
func (s *Service) ImportTemplates(ctx context.Context, tenantID, actor string, raw []byte) ([]Template, error) {
	templates, err := ParseTemplatePack(raw)
	if err != nil {
		return nil, err
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("%w: pack contains no templates", ErrInvalid)
	}
	seen := make(map[string]bool, len(templates))
	for _, template := range templates {
		key := template.ID + "@" + template.Version
		if seen[key] {
			return nil, fmt.Errorf("%w: pack contains duplicate template %s/%s",
				ErrInvalid, template.ID, template.Version)
		}
		seen[key] = true
		if _, err := s.store.GetTemplate(ctx, tenantID, template.ID, template.Version); err == nil {
			return nil, fmt.Errorf("%w: template %s/%s already exists",
				ErrConflict, template.ID, template.Version)
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	imported := make([]Template, 0, len(templates))
	for _, template := range templates {
		created, err := s.CreateTemplate(ctx, tenantID, actor, template)
		if err != nil {
			return nil, err
		}
		imported = append(imported, created)
	}
	return imported, nil
}

// ExportTemplates returns every tenant template as a portable pack.
func (s *Service) ExportTemplates(ctx context.Context, tenantID string) (TemplatePack, error) {
	templates, err := s.ListTemplates(ctx, tenantID)
	if err != nil {
		return TemplatePack{}, err
	}
	return TemplatePack{Templates: templates}, nil
}
