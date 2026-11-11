// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"fmt"
	"math"
	"strconv"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// Apply converts one raw value using the descriptor and validates its
// engineering range.
func Apply(descriptor Descriptor, raw float64, runtime *scriptruntime.Runtime) (Result, error) {
	result, err := convert(descriptor, raw, runtime)
	if err != nil {
		return Result{}, err
	}
	if !result.IsString {
		result.Double = round(result.Double, descriptor.Precision)
	}
	result.InRange = inRange(result, descriptor)
	return result, nil
}

// convert dispatches the configured transformation.
func convert(descriptor Descriptor, raw float64, runtime *scriptruntime.Runtime) (Result, error) {
	switch descriptor.TransformType {
	case TransformIdentity:
		return Result{Double: raw}, nil
	case TransformLinear:
		return Result{Double: raw*descriptor.Scale + descriptor.Offset}, nil
	case TransformTable:
		return tableLookup(descriptor.Breakpoints, raw)
	case TransformEnum:
		return enumLookup(descriptor.EnumMap, raw)
	case TransformExpression:
		return expressionLookup(descriptor.Expression, raw, runtime)
	default:
		return Result{}, fmt.Errorf("unknown transform type %q", descriptor.TransformType)
	}
}

// tableLookup performs exact or linear interpolation on sorted points.
func tableLookup(points []Breakpoint, raw float64) (Result, error) {
	if len(points) == 0 {
		return Result{}, fmt.Errorf("table transform has no breakpoints")
	}
	if raw < points[0].X || raw > points[len(points)-1].X {
		return Result{}, fmt.Errorf("raw value %g is outside the breakpoint table", raw)
	}
	for i := 0; i < len(points)-1; i++ {
		left := points[i]
		right := points[i+1]
		if raw == left.X {
			return Result{Double: left.Y}, nil
		}
		if raw > left.X && raw <= right.X {
			ratio := (raw - left.X) / (right.X - left.X)
			return Result{Double: left.Y + ratio*(right.Y-left.Y)}, nil
		}
	}
	return Result{Double: points[len(points)-1].Y}, nil
}

// enumLookup resolves a numeric code to a textual state.
func enumLookup(values map[string]string, raw float64) (Result, error) {
	key := strconv.FormatFloat(raw, 'g', -1, 64)
	value, ok := values[key]
	if !ok {
		return Result{}, fmt.Errorf("enum code %s is not declared", key)
	}
	return Result{String: value, IsString: true}, nil
}

// expressionLookup evaluates a user expression returning a number or string.
func expressionLookup(source string, raw float64, runtime *scriptruntime.Runtime) (Result, error) {
	if runtime == nil {
		return Result{}, fmt.Errorf("expression transform runtime is unavailable")
	}
	value, err := runtime.EvalValue(source, map[string]any{"raw": raw})
	if err != nil {
		return Result{}, err
	}
	switch typed := value.(type) {
	case float64:
		return Result{Double: typed}, nil
	case int64:
		return Result{Double: float64(typed)}, nil
	case string:
		return Result{String: typed, IsString: true}, nil
	default:
		return Result{}, fmt.Errorf("expression must return a number or string")
	}
}

// round rounds to the requested decimal precision.
func round(value float64, precision int) float64 {
	if precision < 0 {
		return value
	}
	factor := math.Pow(10, float64(precision))
	return math.Round(value*factor) / factor
}

// inRange validates either a numeric or textual conversion result.
func inRange(result Result, descriptor Descriptor) bool {
	if result.IsString {
		return true
	}
	if descriptor.ValidMin != nil && result.Double < *descriptor.ValidMin {
		return false
	}
	if descriptor.ValidMax != nil && result.Double > *descriptor.ValidMax {
		return false
	}
	return true
}
