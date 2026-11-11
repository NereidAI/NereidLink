// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"strings"

	"github.com/nereidai/nereidlink/internal/registry"
)

// translatableValueTypes is the EdgeX scalar value-type set NL turns into a
// measurement descriptor. Booleans and strings are stored verbatim (v_bool /
// v_string); integers ride the float64 column as JSON numbers (no v_int64).
// Binary, Object and every *Array / *Object value type are intentionally
// excluded: the raw profile remains cached, only descriptor translation
// skips them.
var translatableValueTypes = map[string]bool{
	"Bool":    true,
	"String":  true,
	"Int8":    true,
	"Int16":   true,
	"Int32":   true,
	"Int64":   true,
	"Uint8":   true,
	"Uint16":  true,
	"Uint32":  true,
	"Uint64":  true,
	"Float32": true,
	"Float64": true,
}

// skipReason classifies a value type NL does not translate.
func skipReason(valueType string) (string, bool) {
	vt := strings.TrimSpace(valueType)
	if vt == "" {
		return "empty valueType", true
	}
	if translatableValueTypes[vt] {
		return "", false
	}
	if strings.EqualFold(vt, "Binary") {
		return "binary resources are not modeled as telemetry descriptors", true
	}
	if strings.HasSuffix(vt, "Array") || strings.HasSuffix(vt, "Object") ||
		strings.EqualFold(vt, "Object") {
		return "array/object value types are not modeled in v1", true
	}
	return "unsupported EdgeX value type " + vt, true
}

// Translate converts an edge profile's device resources into native
// measurement descriptors plus a list of skipped resources. Every translated
// descriptor is an identity transform: the platform carries no business logic,
// and the edge already supplies engineering values with their units. Duplicate
// resource names collapse to the first occurrence (EdgeX forbids duplicates,
// but this keeps a malformed report from failing adopt).
func Translate(profile Profile) ([]registry.DescriptorDTO, []SkippedResource) {
	descriptors := []registry.DescriptorDTO{}
	skipped := []SkippedResource{}
	seen := map[string]bool{}
	for _, resource := range profile.DeviceResources {
		name := strings.TrimSpace(resource.Name)
		if name == "" {
			skipped = append(skipped, SkippedResource{
				Name: resource.Name, ValueType: resource.ValueType,
				Reason: "empty resource name",
			})
			continue
		}
		if seen[name] {
			skipped = append(skipped, SkippedResource{
				Name: name, ValueType: resource.ValueType,
				Reason: "duplicate resource name",
			})
			continue
		}
		if reason, skip := skipReason(resource.ValueType); skip {
			skipped = append(skipped, SkippedResource{
				Name: name, ValueType: resource.ValueType, Reason: reason,
			})
			continue
		}
		seen[name] = true
		descriptors = append(descriptors, registry.DescriptorDTO{
			MeasurementKey: name,
			Unit:           resource.Units,
			TransformType:  "identity",
			Scale:          1,
			Offset:         0,
			Precision:      -1,
		})
	}
	return descriptors, skipped
}

// ResourceClass is one resource together with the adopt-time decision about
// it. It backs the profile detail view so the UI can show the raw resources
// and why each would or would not become a descriptor, without adopting.
type ResourceClass struct {
	// Resource is the raw reported resource.
	Resource Resource

	// Translatable reports Translate would emit a descriptor for it.
	Translatable bool

	// SkipReason is empty for translatable resources.
	SkipReason string
}

// ClassifyResources returns every reported resource in report order with the
// same decision Translate makes, including the resources Translate drops.
// Duplicate/empty-name handling mirrors Translate exactly so the detail view
// and the actual adopt never disagree.
func ClassifyResources(profile Profile) []ResourceClass {
	out := make([]ResourceClass, 0, len(profile.DeviceResources))
	seen := map[string]bool{}
	for _, resource := range profile.DeviceResources {
		class := ResourceClass{Resource: resource}
		name := strings.TrimSpace(resource.Name)
		switch {
		case name == "":
			class.SkipReason = "empty resource name"
		case seen[name]:
			class.SkipReason = "duplicate resource name"
		default:
			if reason, skip := skipReason(resource.ValueType); skip {
				class.SkipReason = reason
			} else {
				seen[name] = true
				class.Translatable = true
			}
		}
		out = append(out, class)
	}
	return out
}
