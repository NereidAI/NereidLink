// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import "testing"

func TestParseNeutralDocument(t *testing.T) {
	raw := []byte(`{
      "name":"acme-hvac","protocol":"modbus",
      "points":[
        {"name":"run","type":"bool","address":{"primaryTable":"COILS","startingAddress":0}},
        {"name":"temp","type":"float","unit":"C","min":-40,"max":120},
        {"name":"weird","type":"frobnicate"},
        {"name":"","type":"bool"}
      ]}`)
	res, err := ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Name != "acme-hvac" || res.Protocol != "modbus" {
		t.Fatalf("name/protocol wrong: %+v", res)
	}
	if len(res.Upsert.Points) != 3 {
		t.Fatalf("want 3 points (blank skipped), got %d", len(res.Upsert.Points))
	}
	if res.Upsert.Points[0].DataType != "bool" || string(res.Upsert.Points[0].Address) == "" {
		t.Fatalf("address must be preserved: %+v", res.Upsert.Points[0])
	}
	if res.Upsert.Points[2].DataType != "string" {
		t.Fatalf("unknown type must degrade to string: %+v", res.Upsert.Points[2])
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warnings for empty name and unknown type")
	}
}

func TestParseEdgeXDocumentInfModbus(t *testing.T) {
	raw := []byte(`{
      "name":"Test-Device-Modbus-Profile",
      "deviceResources":[
        {"name":"SwitchA","properties":{"valueType":"Bool","readWrite":"RW"},
         "attributes":{"primaryTable":"COILS","startingAddress":0}},
        {"name":"Mode","properties":{"valueType":"Int16","units":""}}
      ]}`)
	res, err := ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Protocol != "modbus" {
		t.Fatalf("modbus family must be inferred from attributes, got %q", res.Protocol)
	}
	if len(res.Upsert.Points) != 2 {
		t.Fatalf("want 2 points, got %d", len(res.Upsert.Points))
	}
	p0 := res.Upsert.Points[0]
	if p0.DataType != "bool" || p0.ReadWrite != "RW" {
		t.Fatalf("EdgeX mapping wrong: %+v", p0)
	}
	if string(p0.Address) == "" {
		t.Fatal("attributes must map to address")
	}
}

func TestParseEdgeXDocumentInferS7(t *testing.T) {
	raw := []byte(`{
      "name":"S7-Profile",
      "deviceResources":[
        {"name":"MotorSpeed","properties":{"valueType":"Int32","readWrite":"R"},
         "attributes":{"NodeName":"DB4.DBD4"}}
      ]}`)
	res, err := ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Protocol != "s7" {
		t.Fatalf("s7 family must be inferred from NodeName, got %q", res.Protocol)
	}
}

func TestParseNeutralDataTypeMapping(t *testing.T) {
	cases := map[string]string{
		"Bool": "bool", "String": "string", "Binary": "binary",
		"Int16": "int", "Uint32": "uint", "Float64": "float",
		"BoolArray": "string", "": "string",
	}
	for in, want := range cases {
		if got := neutralDataType(in); got != want {
			t.Errorf("neutralDataType(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseDocumentRejectsBlankName(t *testing.T) {
	if _, err := ParseDocument([]byte(`{"points":[]}`)); err == nil {
		t.Fatal("blank name must be an error")
	}
	if _, err := ParseDocument([]byte(`{not json`)); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
}
