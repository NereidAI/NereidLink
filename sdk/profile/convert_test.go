// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"testing"
	"time"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

func TestApplyLinear(t *testing.T) {
	descriptor := Descriptor{
		TransformType: TransformLinear,
		Scale:         0.5,
		Offset:        10,
		Precision:     1,
		Version:       2,
	}
	result, err := Apply(descriptor, 20, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Double != 20 || !result.InRange {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestApplyTable(t *testing.T) {
	descriptor := Descriptor{
		TransformType: TransformTable,
		Breakpoints: []Breakpoint{
			{X: 0, Y: 0},
			{X: 10, Y: 10},
			{X: 20, Y: 30},
		},
	}
	result, err := Apply(descriptor, 15, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Double != 20 {
		t.Fatalf("unexpected interpolation: %v", result.Double)
	}
}

func TestApplyEnum(t *testing.T) {
	descriptor := Descriptor{
		TransformType: TransformEnum,
		EnumMap:       map[string]string{"1": "ON", "0": "OFF"},
	}
	result, err := Apply(descriptor, 1, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.IsString || result.String != "ON" {
		t.Fatalf("unexpected enum result: %+v", result)
	}
}

func TestApplyExpression(t *testing.T) {
	runtime := scriptruntime.New(time.Second)
	descriptor := Descriptor{
		TransformType: TransformExpression,
		Expression:    "return raw * 2 + 1;",
	}
	result, err := Apply(descriptor, 4, runtime)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Double != 9 {
		t.Fatalf("unexpected expression result: %v", result.Double)
	}
}

func TestApplyRangeFailure(t *testing.T) {
	maximum := 24.0
	descriptor := Descriptor{
		TransformType: TransformIdentity,
		ValidMax:      &maximum,
	}
	result, err := Apply(descriptor, 25, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.InRange {
		t.Fatal("expected out-of-range result")
	}
}
