// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// edgex.go renders the EdgeX DeviceProfile shape (ADR-0038 §5). It is the
// inverse of internal/edgeprofile.document.go's EdgeX parser: points become
// deviceResources with properties/attributes, and commands become
// deviceCommands with resourceOperations. NL ships this adapter because EdgeX
// device services are the common consumer, but the kernel carries no EdgeX
// concept — this file is the only place the mapping lives.

package profilepush

// edgexRenderer emits an EdgeX DeviceProfile document.
type edgexRenderer struct{}

// Format identifies the EdgeX render format.
func (edgexRenderer) Format() string { return FormatEdgeX }

// edgexValueType maps a neutral type to an EdgeX valueType. Unknown types
// degrade to String, matching the permissive import path.
func edgexValueType(neutral string) string {
	switch neutral {
	case "bool":
		return "Bool"
	case "int":
		return "Int32"
	case "uint":
		return "Uint32"
	case "float":
		return "Float32"
	case "binary":
		return "Binary"
	case "string":
		return "String"
	default:
		return "String"
	}
}

// Render builds the EdgeX DeviceProfile document from the projection.
func (edgexRenderer) Render(p Projection) (map[string]any, error) {
	resources := make([]any, 0, len(p.Points))
	for _, pt := range p.Points {
		properties := map[string]any{
			"valueType": edgexValueType(pt.Type),
		}
		putString(properties, "units", pt.Unit)
		putString(properties, "readWrite", pt.ReadWrite)
		putString(properties, "defaultValue", pt.Default)
		if pt.Min != nil {
			properties["minimum"] = *pt.Min
		}
		if pt.Max != nil {
			properties["maximum"] = *pt.Max
		}
		resource := map[string]any{
			"name":       pt.Name,
			"properties": properties,
			"attributes": emptyObjectOrAddress(pt.Address),
		}
		putString(resource, "description", pt.Description)
		resources = append(resources, resource)
	}
	doc := map[string]any{
		"name":            p.Name,
		"deviceResources": resources,
	}
	putString(doc, "description", p.Description)
	if len(p.Labels) > 0 {
		doc["labels"] = p.Labels
	}
	if len(p.Commands) > 0 {
		commands := make([]any, 0, len(p.Commands))
		for _, cmd := range p.Commands {
			ops := make([]any, 0, len(cmd.Points))
			for _, name := range cmd.Points {
				ops = append(ops, map[string]any{"deviceResource": name})
			}
			commands = append(commands, map[string]any{
				"name":               cmd.Name,
				"readWrite":          "R",
				"resourceOperations": ops,
			})
		}
		doc["deviceCommands"] = commands
	}
	return doc, nil
}
