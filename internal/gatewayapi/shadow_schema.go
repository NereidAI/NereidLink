// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_schema.go derives the console desired-state editor's writable-key
// catalog from the device's bound profile. A profile measurement descriptor
// marked W/RW is a key the platform can set in desired state; read-only or
// inactive points are excluded. This is a read-only projection over existing
// profile metadata — no extra storage.

package gatewayapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/nereidai/nereidlink/internal/registry"
)

// DesiredKeyField is one writable shadow key offered by the editor.
type DesiredKeyField struct {
	// Key is the top-level shadow key (the profile measurement key).
	Key string `json:"key"`

	// Type is the editor value type: boolean, number or string. Neutral
	// profiles carry no object/array points, so only scalars appear.
	Type string `json:"type"`

	// Unit is an optional engineering unit for display.
	Unit string `json:"unit,omitempty"`

	// Min/Max bound numeric keys when the profile reports a range.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`

	// Default is the parsed edge-reported default, offered as the initial
	// form value. It is omitted when the profile reports none.
	Default any `json:"default,omitempty"`
}

// DesiredSchema is the writable-key catalog for one device.
type DesiredSchema struct {
	// DeviceID identifies the shadowed device.
	DeviceID string `json:"device_id"`

	// ProfileID is the bound profile; empty when the device has none.
	ProfileID string `json:"profile_id,omitempty"`

	// Fields is always an array, even empty, so clients can map over it.
	Fields []DesiredKeyField `json:"fields"`
}

// shadowValueType maps a neutral data type to an editor value type.
func shadowValueType(dataType string) string {
	switch dataType {
	case "bool":
		return "boolean"
	case "int", "uint", "float":
		return "number"
	default: // string, binary and unknown are edited as text.
		return "string"
	}
}

// parseDefault parses the edge-reported default text into a typed value
// matching the editor kind. An empty or unparseable default yields nil.
func parseDefault(kind, text string) any {
	if text == "" {
		return nil
	}
	switch kind {
	case "number":
		if v, err := strconv.ParseFloat(text, 64); err == nil {
			return v
		}
	case "boolean":
		if v, err := strconv.ParseBool(text); err == nil {
			return v
		}
	default:
		// Accept a JSON-encoded scalar/string, otherwise treat the raw text
		// itself as the string value.
		var v any
		if err := json.Unmarshal([]byte(text), &v); err == nil {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return text
	}
	return nil
}

// desiredFieldsFromProfile projects writable profile descriptors into shadow
// editor fields.
func desiredFieldsFromProfile(profile registry.ProfileDTO) []DesiredKeyField {
	fields := make([]DesiredKeyField, 0, len(profile.Descriptors))
	for _, d := range profile.Descriptors {
		if d.Inactive || d.MeasurementKey == "" {
			continue
		}
		if d.ReadWrite != "W" && d.ReadWrite != "RW" {
			continue
		}
		kind := shadowValueType(d.DataType)
		field := DesiredKeyField{
			Key:  d.MeasurementKey,
			Type: kind,
			Unit: d.Unit,
			// Prefer the edge-reported engineering range; fall back to the
			// operator-owned validation range.
			Min: d.EdgeMin,
			Max: d.EdgeMax,
		}
		if field.Min == nil {
			field.Min = d.ValidMin
		}
		if field.Max == nil {
			field.Max = d.ValidMax
		}
		if v := parseDefault(kind, d.EdgeDefault); v != nil {
			field.Default = v
		}
		fields = append(fields, field)
	}
	return fields
}

// handleShadowDesiredSchema returns the writable-key catalog derived from the
// device's bound profile. A device without a profile yields an empty field
// list rather than an error.
func (h *Hub) handleShadowDesiredSchema(w http.ResponseWriter, r *http.Request) {
	if !h.registryReady(w) {
		return
	}
	tenantID := r.PathValue("tenant")
	deviceID := r.PathValue("device")
	device, err := h.registryAdmin.GetDevice(r.Context(), tenantID, deviceID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	schema := DesiredSchema{DeviceID: deviceID, Fields: []DesiredKeyField{}}
	if device.ProfileID == "" {
		writeRESTJSON(w, http.StatusOK, schema)
		return
	}
	profile, err := h.registryAdmin.GetProfile(r.Context(), tenantID, device.ProfileID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	schema.ProfileID = profile.ID
	schema.Fields = desiredFieldsFromProfile(profile)
	writeRESTJSON(w, http.StatusOK, schema)
}
