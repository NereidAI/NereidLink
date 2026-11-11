// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// render_test.go pins the render contract: the neutral document matches the
// ADR-0032 uplink shape, the EdgeX document round-trips back through the
// uplink parser without losing NL-owned fields, and the projection hash is
// stable and insensitive to value-add fields the projection never carries.

package profilepush

import (
	"encoding/json"
	"testing"

	"github.com/nereidai/nereidlink/internal/edgeprofile"
)

func fp(v float64) *float64 { return &v }

// sampleProjection returns a projection exercising every mapped field.
func sampleProjection() Projection {
	return Projection{
		Name:        "A1",
		Protocol:    "modbus",
		Description: "Boiler room A",
		Labels:      []string{"boiler", "line-1"},
		Points: []Point{
			{
				Name: "temperature", Type: "float", Unit: "Cel",
				ReadWrite: "R", Min: fp(-40), Max: fp(150),
				Description: "Inlet temperature",
				Address:     json.RawMessage(`{"slave_id":1,"function":3,"offset":10}`),
			},
			{
				Name: "setpoint", Type: "int", ReadWrite: "RW",
				Default: "20",
				Address: json.RawMessage(`{"slave_id":1,"function":6,"offset":11}`),
			},
		},
		Commands: []Command{{Name: "read_all", Points: []string{"temperature", "setpoint"}}},
	}
}

// TestNeutralRenderShape asserts the neutral document is the ADR-0032 §3 shape
// and that an unset address is emitted as {} rather than null.
func TestNeutralRenderShape(t *testing.T) {
	doc, err := Render(FormatNeutral, sampleProjection())
	if err != nil {
		t.Fatalf("render neutral: %v", err)
	}
	if doc["name"] != "A1" || doc["protocol"] != "modbus" {
		t.Fatalf("unexpected header: %+v", doc)
	}
	points, ok := doc["points"].([]any)
	if !ok || len(points) != 2 {
		t.Fatalf("expected 2 points, got %#v", doc["points"])
	}
	first := points[0].(map[string]any)
	if first["name"] != "temperature" || first["type"] != "float" {
		t.Fatalf("unexpected first point: %+v", first)
	}
	if first["min"] != float64(-40) || first["max"] != float64(150) {
		t.Fatalf("range not rendered: %+v", first)
	}
	addr, ok := first["address"].(map[string]any)
	if !ok || addr["offset"] != float64(10) {
		t.Fatalf("address not rendered: %#v", first["address"])
	}
	cmds := doc["commands"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %#v", doc["commands"])
	}
}

// TestNeutralEmptyAddressIsObject runs the whole projection through JSON and
// asserts address is {} (never null) when unset.
func TestNeutralEmptyAddressIsObject(t *testing.T) {
	p := Projection{Name: "empty", Points: []Point{{Name: "x", Type: "string"}}}
	doc, err := Render(FormatNeutral, p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pt := generic["points"].([]any)[0].(map[string]any)
	if pt["address"] == nil {
		t.Fatal("address must never be null")
	}
	if _, ok := pt["address"].(map[string]any); !ok {
		t.Fatalf("address must be an object, got %#v", pt["address"])
	}
}

// TestEdgeXRoundTrip renders the EdgeX document and re-parses it with the
// uplink parser, asserting the NL-owned fields survive.
func TestEdgeXRoundTrip(t *testing.T) {
	doc, err := Render(FormatEdgeX, sampleProjection())
	if err != nil {
		t.Fatalf("render edgex: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := edgeprofile.ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if result.Name != "A1" {
		t.Fatalf("name lost: %q", result.Name)
	}
	if len(result.Upsert.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(result.Upsert.Points))
	}
	temp := result.Upsert.Points[0]
	if temp.Name != "temperature" || temp.DataType != "float" {
		t.Fatalf("name/type lost: %+v", temp)
	}
	if temp.Unit != "Cel" || temp.ReadWrite != "R" {
		t.Fatalf("unit/read_write lost: %+v", temp)
	}
	if temp.EdgeMin == nil || *temp.EdgeMin != -40 || temp.EdgeMax == nil || *temp.EdgeMax != 150 {
		t.Fatalf("range lost: %+v", temp)
	}
	var addr map[string]any
	if err := json.Unmarshal(temp.Address, &addr); err != nil {
		t.Fatalf("address not decodable: %v", err)
	}
	if addr["offset"] != float64(10) {
		t.Fatalf("address lost: %#v", addr)
	}
}

// TestSelectFormat covers preference ordering, unknown formats and the empty
// default.
func TestSelectFormat(t *testing.T) {
	cases := []struct {
		name      string
		preferred []string
		want      string
	}{
		{"empty defaults to neutral", nil, FormatNeutral},
		{"unknown defaults to neutral", []string{"vendor/x"}, FormatNeutral},
		{"first supported wins", []string{"edgex-device-profile/3", "nereid-neutral/1"}, FormatEdgeX},
		{"skips unknown", []string{"vendor/x", "nereid-neutral/1"}, FormatNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectFormat(tc.preferred); got != tc.want {
				t.Fatalf("SelectFormat(%v) = %q, want %q", tc.preferred, got, tc.want)
			}
		})
	}
}

// TestProjectionHashStableAndValueAddInsensitive asserts the hash is stable for
// equal projections and that the projection has no place to carry value-add
// fields, so operator calibration cannot change a revision.
func TestProjectionHashStableAndValueAddInsensitive(t *testing.T) {
	a := sampleProjection()
	b := sampleProjection()
	ha, err := projectionHash(libraryOf(a))
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hb, err := projectionHash(libraryOf(b))
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ha != hb {
		t.Fatalf("hash not stable: %s != %s", ha, hb)
	}
	// A point change must change the hash.
	c := sampleProjection()
	c.Points[0].Unit = "K"
	hc, err := projectionHash(libraryOf(c))
	if err != nil {
		t.Fatalf("hash c: %v", err)
	}
	if hc == ha {
		t.Fatal("hash must change when an edge-visible field changes")
	}
}

// libraryOf adapts a projection into a LibraryProfile for hashing.
func libraryOf(p Projection) LibraryProfile {
	return LibraryProfile{
		Name: p.Name, Protocol: p.Protocol, Description: p.Description,
		Labels: p.Labels, Points: p.Points, Commands: p.Commands,
	}
}
