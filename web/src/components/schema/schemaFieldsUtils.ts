// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// schemaFieldsUtils.ts extracts a flat, renderable field model from a
// draft 2020-12 JSON Schema. The mapping is a pure function so it is
// testable without rendering; the form controls live in SchemaFields.tsx.

export type FieldKind = "string" | "password" | "number" | "boolean" | "select";

// SchemaField is the extracted view of one JSON Schema property.
export interface SchemaField {
  key: string;
  label: string;
  kind: FieldKind;
  required: boolean;
  description?: string;
  defaultValue?: unknown;
  options?: { value: string; label: string }[];
}

// jsonSchema is the minimal draft 2020-12 shape the extractor reads.
interface JsonSchema {
  type?: string | string[];
  format?: string;
  title?: string;
  description?: string;
  enum?: unknown[];
  default?: unknown;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  [key: string]: unknown;
}

// isSecret reports whether a property must render as a password control:
// JSON Schema has no native secret marker, so both format="password" and the
// custom annotation x-secret are accepted (edge schemas use either).
function isSecret(prop: JsonSchema): boolean {
  return prop.format === "password" || prop["x-secret"] === true;
}

// fieldKind maps a property schema to a control kind.
function fieldKind(prop: JsonSchema): FieldKind {
  if (Array.isArray(prop.enum) && prop.enum.length > 0) return "select";
  if (prop.type === "integer" || prop.type === "number") return "number";
  if (prop.type === "boolean") return "boolean";
  if (isSecret(prop)) return "password";
  return "string";
}

// extractFields maps the top-level properties of an object JSON Schema to
// field models in declaration order. A non-object or empty schema produces
// no fields (the caller then falls back to the generic key/value editor).
export function extractFields(rawSchema: Record<string, unknown>): SchemaField[] {
  const schema = rawSchema as JsonSchema;
  if (schema.type !== "object" || !schema.properties) return [];
  const required = new Set(schema.required ?? []);
  const fields: SchemaField[] = [];
  for (const [key, prop] of Object.entries(schema.properties)) {
    const field: SchemaField = {
      key,
      label: prop.title || key,
      kind: fieldKind(prop),
      required: required.has(key),
      description: prop.description,
      defaultValue: prop.default,
    };
    if (field.kind === "select") {
      field.options = (prop.enum ?? []).map((value) => {
        const text = String(value);
        return { value: text, label: text };
      });
    }
    fields.push(field);
  }
  return fields;
}

// hasRenderableSchema reports whether the schema yields a typed form.
export function hasRenderableSchema(rawSchema: Record<string, unknown>): boolean {
  return extractFields(rawSchema).length > 0;
}
