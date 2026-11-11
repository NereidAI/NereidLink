// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// render.go turns the tenant library profile into the document a given gateway
// declared it accepts (ADR-0038 §5). It is the single place that knows any
// wire shape: the kernel stores a neutral projection and each renderer is a
// pure function, so adding a format never touches delivery or the kernel.
//
// The neutral format is the contract a third-party edge implements and is
// byte-compatible with the ADR-0032 §3 uplink shape. The EdgeX renderer is the
// inverse of internal/edgeprofile.document.go, kept symmetric so a round trip
// (EdgeX -> neutral -> EdgeX) preserves the fields NL owns.

package profilepush

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Projection is the edge-visible subset of a library profile. It is the only
// input to rendering: operator value-add fields (transform/scale/enum/
// breakpoints/valid range) are absent by construction, so calibration edits
// never change a rendered document or a revision.
type Projection struct {
	// Name is the tenant-unique profile name.
	Name string
	// Protocol is the open-vocabulary protocol family ("" unknown).
	Protocol string
	// Description is the optional human description.
	Description string
	// Labels are optional free-form tags.
	Labels []string
	// Points is the ordered point set.
	Points []Point
	// Commands groups points under a name.
	Commands []Command
}

// Point is one edge-visible point.
type Point struct {
	Name        string
	Type        string
	Unit        string
	ReadWrite   string
	Min         *float64
	Max         *float64
	Default     string
	Description string
	// Address is the opaque per-protocol addressing object; emitted as {}
	// when empty, never null.
	Address json.RawMessage
}

// Command groups several points under one name.
type Command struct {
	Name   string
	Points []string
}

// Renderer renders a projection into one wire document.
type Renderer interface {
	// Format is the identifier the gateway must declare to receive it.
	Format() string
	// Render returns the document object for one profile.
	Render(p Projection) (map[string]any, error)
}

// renderers holds the built-in formats by identifier.
var renderers = map[string]Renderer{
	FormatNeutral: neutralRenderer{},
	FormatEdgeX:   edgexRenderer{},
}

// SupportedFormats lists every built-in render format identifier.
func SupportedFormats() []string {
	return []string{FormatNeutral, FormatEdgeX}
}

// SelectFormat picks the first gateway-preferred format the platform can
// render. When the gateway declared none, the neutral format is used; an
// unknown preference is skipped rather than failing the delivery.
func SelectFormat(preferred []string) string {
	for _, want := range preferred {
		if _, ok := renderers[strings.TrimSpace(want)]; ok {
			return strings.TrimSpace(want)
		}
	}
	return FormatNeutral
}

// Render renders a projection in the requested format, falling back to the
// neutral format when the identifier is unknown.
func Render(format string, p Projection) (map[string]any, error) {
	r, ok := renderers[format]
	if !ok {
		r = renderers[FormatNeutral]
	}
	return r.Render(p)
}

// canonicalJSON marshals a document with stable key ordering (Go maps marshal
// deterministically) so the desired hash is reproducible across replicas.
func canonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// sha256Hex returns the lowercase hex sha256 of raw.
func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// emptyObjectOrAddress returns the opaque address as a decoded object, or an
// empty object when unset, so the wire never carries null.
func emptyObjectOrAddress(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
