// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package profile implements device profile resolution and declarative
// raw-to-engineering conversion used by the Storage Pipeline.
package profile

const (
	// TransformIdentity stores the raw value unchanged.
	TransformIdentity = "identity"

	// TransformLinear applies scale and offset.
	TransformLinear = "linear"

	// TransformTable interpolates ordered breakpoint pairs.
	TransformTable = "table"

	// TransformExpression evaluates a sandboxed JavaScript expression.
	TransformExpression = "expression"

	// TransformEnum maps numeric codes to textual states.
	TransformEnum = "enum"
)

// Profile is the device profile projection required by ingress.
type Profile struct {
	// ID is the globally unique profile identifier.
	ID string

	// TenantID owns the profile.
	TenantID string

	// Name is the unique tenant-scoped profile name.
	Name string
}

// Breakpoint is one point in a non-linear lookup table.
type Breakpoint struct {
	// X is the raw input value.
	X float64 `json:"x"`

	// Y is the engineering output value.
	Y float64 `json:"y"`
}

// Descriptor declares conversion rules for one measurement.
type Descriptor struct {
	// ID is the descriptor identifier.
	ID string

	// TenantID owns the descriptor.
	TenantID string

	// ProfileID is the profile containing the descriptor.
	ProfileID string

	// MeasurementKey identifies the measurement in telemetry frames.
	MeasurementKey string

	// Unit is the engineering unit.
	Unit string

	// TransformType selects the conversion algorithm.
	TransformType string

	// Scale is used by linear conversion.
	Scale float64

	// Offset is used by linear conversion.
	Offset float64

	// Breakpoints are used by table conversion.
	Breakpoints []Breakpoint

	// EnumMap maps raw codes to textual states.
	EnumMap map[string]string

	// Expression is the source used by expression conversion.
	Expression string

	// Precision is the decimal precision of the engineering value.
	Precision int

	// ValidMin optionally bounds the engineering value.
	ValidMin *float64

	// ValidMax optionally bounds the engineering value.
	ValidMax *float64

	// Version travels with every converted telemetry row.
	Version int
}

// Override applies device-specific calibration to a profile descriptor.
type Override struct {
	// Scale overrides the profile linear scale.
	Scale float64

	// Offset overrides the profile linear offset.
	Offset float64

	// ValidMin optionally overrides the profile minimum.
	ValidMin *float64

	// ValidMax optionally overrides the profile maximum.
	ValidMax *float64

	// Precision optionally overrides the profile precision.
	Precision int
}

// Result is the output of a descriptor conversion.
type Result struct {
	// Double is the numeric engineering result.
	Double float64

	// String is the textual engineering result.
	String string

	// IsString reports whether String should be used.
	IsString bool

	// InRange reports whether the result satisfies the valid range.
	InRange bool
}
