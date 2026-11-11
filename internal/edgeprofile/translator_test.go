// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

func TestTranslate(t *testing.T) {
	profile := Profile{
		Name: "P",
		DeviceResources: []Resource{
			{Name: "running", ValueType: "Bool"},
			{Name: "temp", ValueType: "Float64", Units: "C"},
			{Name: "code", ValueType: "Int32"},
			{Name: "label", ValueType: "String"},
			{Name: "blob", ValueType: "Binary"},
			{Name: "samples", ValueType: "Float64Array"},
			{Name: "obj", ValueType: "Object"},
			{Name: "", ValueType: "Bool"},
			{Name: "temp", ValueType: "Float64", Units: "C"},
			{Name: "weird", ValueType: "Mystery"},
		},
	}
	descriptors, skipped := Translate(profile)

	keys := map[string]registry.DescriptorDTO{}
	for _, d := range descriptors {
		if d.TransformType != "identity" || d.Scale != 1 || d.Precision != -1 {
			t.Fatalf("descriptor %s not a default identity: %+v", d.MeasurementKey, d)
		}
		keys[d.MeasurementKey] = d
	}
	for _, want := range []string{"running", "temp", "code", "label"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("missing translated descriptor %q: %+v", want, descriptors)
		}
	}
	if keys["temp"].Unit != "C" {
		t.Fatalf("unit not carried: %+v", keys["temp"])
	}

	skippedNames := map[string]bool{}
	for _, s := range skipped {
		skippedNames[s.Name] = true
		if s.Reason == "" {
			t.Fatalf("skipped resource %q has no reason", s.Name)
		}
	}
	for _, want := range []string{"blob", "samples", "obj", "weird"} {
		if !skippedNames[want] {
			t.Fatalf("expected %q to be skipped: %+v", want, skipped)
		}
	}
	if len(descriptors) != 4 {
		t.Fatalf("want 4 descriptors, got %d", len(descriptors))
	}
	// First duplicate collapses; the empty-name entry is separately skipped.
	if len(skipped) != 6 {
		t.Fatalf("want 6 skipped, got %d: %+v", len(skipped), skipped)
	}
}

func TestCanonicalHashAgreement(t *testing.T) {
	a := Profile{Name: "P", DeviceResources: []Resource{{Name: "b", ValueType: "Bool"}}}
	b := Profile{Name: "P", DeviceResources: []Resource{{Name: "b", ValueType: "Bool", Units: ""}}}
	ha, err := CanonicalHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("omitempty-equivalent profiles must hash equally: %s vs %s", ha, hb)
	}
}

func TestClassifyMatchesTranslate(t *testing.T) {
	profile := Profile{Name: "P", DeviceResources: []Resource{
		{Name: "b", ValueType: "Bool"},
		{Name: "n", ValueType: "Float64", Units: "C"},
		{Name: "blob", ValueType: "Binary"},
		{Name: "arr", ValueType: "Int32Array"},
		{Name: "b", ValueType: "Bool"},
		{Name: "", ValueType: "Bool"},
	}}
	descriptors, skipped := Translate(profile)
	classes := ClassifyResources(profile)

	if len(classes) != len(profile.DeviceResources) {
		t.Fatalf("classify must keep every raw resource: got %d want %d",
			len(classes), len(profile.DeviceResources))
	}
	translatable := 0
	for _, c := range classes {
		if c.Translatable {
			if c.SkipReason != "" {
				t.Fatalf("translatable resource must carry no skip reason: %+v", c)
			}
			translatable++
		} else if c.SkipReason == "" {
			t.Fatalf("non-translatable resource must explain why: %+v", c)
		}
	}
	if translatable != len(descriptors) {
		t.Fatalf("translatable count %d != Translate descriptor count %d",
			translatable, len(descriptors))
	}
	skippedClassified := 0
	for _, c := range classes {
		if !c.Translatable {
			skippedClassified++
		}
	}
	if skippedClassified != len(skipped) {
		t.Fatalf("skipped classify=%d translate=%d", skippedClassified, len(skipped))
	}
}
