// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// document.go parses a pasted device profile into the vendor-neutral library
// model. Two input shapes are accepted so third-party edges and EdgeX users
// have the same import door:
//
//   - The neutral NL spec (ADR-0032 §3): {name, protocol, points[], commands[]}.
//   - The EdgeX DeviceProfile shape: {name, protocol?, deviceResources[]}, with
//     properties{valueType,...} and attributes.
//
// Parsing is permissive (ADR-0032 §9): structural problems are collected as
// warnings and unknown types degrade to "string" rather than hard-failing.
package edgeprofile

import (
	"encoding/json"
	"strings"

	"github.com/nereidai/nereidlink/internal/registry"
)

// NeutralPoint is one point in an imported neutral document.
type NeutralPoint struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Unit        string          `json:"unit,omitempty"`
	ReadWrite   string          `json:"read_write,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Default     string          `json:"default,omitempty"`
	Description string          `json:"description,omitempty"`
	Address     json.RawMessage `json:"address,omitempty"`
}

// NeutralCommand groups several points under one name.
type NeutralCommand struct {
	Name   string   `json:"name"`
	Points []string `json:"points"`
}

// NeutralDocument is the vendor-neutral profile import payload.
type NeutralDocument struct {
	Name        string           `json:"name"`
	Protocol    string           `json:"protocol,omitempty"`
	Description string           `json:"description,omitempty"`
	Labels      []string         `json:"labels,omitempty"`
	Points      []NeutralPoint   `json:"points"`
	Commands    []NeutralCommand `json:"commands,omitempty"`
}

// edgexResource mirrors the subset of an EdgeX deviceResource that carries
// addressing and typing, used only when an EdgeX-shaped document is pasted.
type edgexResource struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Properties  struct {
		ValueType  string   `json:"valueType"`
		Units      string   `json:"units"`
		ReadWrite  string   `json:"readWrite"`
		Min        *float64 `json:"minimum"`
		Max        *float64 `json:"maximum"`
		DefaultVal string   `json:"defaultValue"`
	} `json:"properties"`
	Attributes map[string]any `json:"attributes"`
}

// edgexDocument is the accepted EdgeX DeviceProfile subset.
type edgexDocument struct {
	Name        string          `json:"name"`
	Protocol    string          `json:"protocol,omitempty"`
	Description string          `json:"description"`
	Resources   []edgexResource `json:"deviceResources"`
}

// ParseResult is a parsed import: a registry upsert plus non-fatal warnings.
type ParseResult struct {
	// Name is the resolved profile name.
	Name string
	// Protocol is the declared or inferred protocol family.
	Protocol string
	// Upsert is the ready-to-apply registry upsert (hash filled by caller).
	Upsert registry.EdgeProfileUpsert
	// Warnings are non-fatal parse notices (unknown types, duplicates, ...).
	Warnings []string
}

var neutralTypes = map[string]bool{
	"bool": true, "int": true, "uint": true,
	"float": true, "string": true, "binary": true,
}

// ParseDocument parses either a neutral or EdgeX profile document. It never
// returns a partial result on hard structural errors (bad JSON / blank name /
// points not an array); everything softer is a warning.
func ParseDocument(raw []byte) (ParseResult, error) {
	var head struct {
		Name           string          `json:"name"`
		Protocol       string          `json:"protocol"`
		Points         json.RawMessage `json:"points"`
		DeviceResource json.RawMessage `json:"deviceResources"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ParseResult{}, err
	}
	if strings.TrimSpace(head.Name) == "" {
		return ParseResult{}, ErrInvalid
	}
	if len(head.Points) > 0 && string(head.Points) != "null" {
		return parseNeutral(raw)
	}
	return parseEdgeX(raw)
}

func parseNeutral(raw []byte) (ParseResult, error) {
	var doc NeutralDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ParseResult{}, err
	}
	warnings := []string{}
	points := make([]registry.EdgePoint, 0, len(doc.Points))
	seen := map[string]bool{}
	for _, p := range doc.Points {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			warnings = append(warnings, "a point with an empty name was skipped")
			continue
		}
		if seen[name] {
			warnings = append(warnings, "duplicate point name skipped: "+name)
			continue
		}
		seen[name] = true
		dataType := strings.ToLower(strings.TrimSpace(p.Type))
		if dataType == "" {
			dataType = "string"
			warnings = append(warnings, "point "+name+" had no type; defaulted to string")
		} else if !neutralTypes[dataType] {
			warnings = append(warnings, "point "+name+" has unknown type "+dataType+"; stored as string")
			dataType = "string"
		}
		points = append(points, registry.EdgePoint{
			Name: name, DataType: dataType, Unit: p.Unit, ReadWrite: p.ReadWrite,
			EdgeMin: p.Min, EdgeMax: p.Max, EdgeDefault: p.Default,
			Address: p.Address,
		})
	}
	upsert := registry.EdgeProfileUpsert{
		Name:     strings.TrimSpace(doc.Name),
		Protocol: strings.TrimSpace(doc.Protocol),
		Payload:  raw,
		Points:   points,
	}
	if upsert.Protocol == "" {
		upsert.Protocol = inferProtocolFamily(points)
	}
	return ParseResult{
		Name: upsert.Name, Protocol: upsert.Protocol,
		Upsert: upsert, Warnings: warnings,
	}, nil
}

func parseEdgeX(raw []byte) (ParseResult, error) {
	var doc edgexDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ParseResult{}, err
	}
	warnings := []string{}
	points := make([]registry.EdgePoint, 0, len(doc.Resources))
	seen := map[string]bool{}
	for _, r := range doc.Resources {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			warnings = append(warnings, "a resource with an empty name was skipped")
			continue
		}
		if seen[name] {
			warnings = append(warnings, "duplicate resource name skipped: "+name)
			continue
		}
		seen[name] = true
		var address json.RawMessage
		if len(r.Attributes) > 0 {
			address, _ = json.Marshal(r.Attributes)
		}
		dataType := neutralDataType(r.Properties.ValueType)
		if _, skip := skipReason(r.Properties.ValueType); skip && r.Properties.ValueType != "" {
			warnings = append(warnings, "resource "+name+" type "+r.Properties.ValueType+" stored as string")
		}
		points = append(points, registry.EdgePoint{
			Name: name, DataType: dataType, Unit: r.Properties.Units,
			ReadWrite: r.Properties.ReadWrite, EdgeMin: r.Properties.Min,
			EdgeMax: r.Properties.Max, EdgeDefault: r.Properties.DefaultVal,
			Address: address,
		})
	}
	protocol := strings.TrimSpace(doc.Protocol)
	if protocol == "" {
		protocol = inferProtocolFamily(points)
	}
	upsert := registry.EdgeProfileUpsert{
		Name: strings.TrimSpace(doc.Name), Protocol: protocol,
		Payload: raw, Points: points,
	}
	return ParseResult{
		Name: upsert.Name, Protocol: protocol,
		Upsert: upsert, Warnings: warnings,
	}, nil
}
