// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// neutral.go renders the vendor-neutral profile document (ADR-0032 §3). It is
// byte-compatible with the uplink shape a gateway reports on v1/gateway/profiles
// and is always available, so a third-party edge only has to understand one
// document shape to consume pushed profiles.

package profilepush

// neutralRenderer emits the ADR-0032 §3 neutral document.
type neutralRenderer struct{}

// Format identifies the neutral render format.
func (neutralRenderer) Format() string { return FormatNeutral }

// Render builds the neutral document from the projection.
func (neutralRenderer) Render(p Projection) (map[string]any, error) {
	points := make([]any, 0, len(p.Points))
	for _, pt := range p.Points {
		point := map[string]any{
			"name":    pt.Name,
			"type":    pt.Type,
			"address": emptyObjectOrAddress(pt.Address),
		}
		putString(point, "unit", pt.Unit)
		putString(point, "read_write", pt.ReadWrite)
		putString(point, "default", pt.Default)
		putString(point, "description", pt.Description)
		if pt.Min != nil {
			point["min"] = *pt.Min
		}
		if pt.Max != nil {
			point["max"] = *pt.Max
		}
		points = append(points, point)
	}
	doc := map[string]any{
		"name":   p.Name,
		"points": points,
	}
	putString(doc, "protocol", p.Protocol)
	putString(doc, "description", p.Description)
	if len(p.Labels) > 0 {
		doc["labels"] = p.Labels
	}
	if len(p.Commands) > 0 {
		commands := make([]any, 0, len(p.Commands))
		for _, cmd := range p.Commands {
			commands = append(commands, map[string]any{
				"name":   cmd.Name,
				"points": cmd.Points,
			})
		}
		doc["commands"] = commands
	}
	return doc, nil
}

// putString sets key only when value is non-empty, keeping the wire compact
// and the document stable for hashing.
func putString(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}
