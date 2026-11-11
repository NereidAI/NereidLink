// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// neutral.go maps the on-the-wire device profile (today the minimal EdgeX
// subset; Phase B extends the frame with addressing) into the vendor-neutral
// library model consumed by the registry. The mapping lives here so the
// registry never learns EdgeX value-type vocabulary: that knowledge belongs
// to the edge-facing package only.
package edgeprofile

import (
	"encoding/json"
	"strings"

	"github.com/nereidai/nereidlink/internal/registry"
)

// neutralDataType maps an EdgeX value type to the neutral spec enum
// bool|int|uint|float|string|binary. Unknown/empty types map to "string"
// (the permissive import rule, ADR-0032 §9) rather than failing the batch.
func neutralDataType(valueType string) string {
	vt := strings.TrimSpace(valueType)
	switch {
	case strings.EqualFold(vt, "Bool"):
		return "bool"
	case strings.EqualFold(vt, "String"):
		return "string"
	case strings.EqualFold(vt, "Binary"):
		return "binary"
	case strings.HasPrefix(vt, "Float"):
		return "float"
	case strings.HasPrefix(vt, "Uint"):
		return "uint"
	case strings.HasPrefix(vt, "Int"):
		return "int"
	case strings.HasSuffix(vt, "Array"), strings.HasSuffix(vt, "Object"),
		strings.EqualFold(vt, "Object"), vt == "":
		// Complex/empty types have no first-class neutral scalar; keep them as
		// an opaque string point so the point is still visible in the library.
		return "string"
	default:
		return "string"
	}
}

// inferProtocolFamily guesses the protocol family from point addressing keys
// (ADR-0032 §6). It returns "" when nothing is recognizable, leaving the
// family unknown so the operator can set it explicitly.
func inferProtocolFamily(points []registry.EdgePoint) string {
	hasModbus, hasOPCUA, hasS7 := false, false, false
	for _, p := range points {
		if len(p.Address) == 0 {
			continue
		}
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(p.Address, &attrs); err != nil {
			continue
		}
		_, hasTable := attrs["primaryTable"]
		_, hasStart := attrs["startingAddress"]
		if hasTable && hasStart {
			hasModbus = true
		}
		if _, ok := attrs["nodeId"]; ok {
			hasOPCUA = true
		}
		// device-s7 reads only attributes.NodeName (e.g. "DB4.DBX0.0");
		// the other address constants in its source are dead code.
		if _, ok := attrs["NodeName"]; ok {
			hasS7 = true
		}
	}
	switch {
	case hasModbus:
		return "modbus"
	case hasOPCUA:
		return "opcua"
	case hasS7:
		return "s7"
	default:
		return ""
	}
}

// toUpsert converts a reported profile into the registry neutral upsert. The
// current minimal frame only carries name/valueType/units/description, so
// addressing is empty and the family is unknown; Phase B fills address/min/max.
func toUpsert(profile Profile, hash string, payload json.RawMessage) registry.EdgeProfileUpsert {
	points := make([]registry.EdgePoint, 0, len(profile.DeviceResources))
	for _, r := range profile.DeviceResources {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			continue
		}
		points = append(points, registry.EdgePoint{
			Name:     name,
			DataType: neutralDataType(r.ValueType),
			Unit:     r.Units,
		})
	}
	return registry.EdgeProfileUpsert{
		Name:        strings.TrimSpace(profile.Name),
		ContentHash: hash,
		Payload:     payload,
		Points:      points,
	}
}
