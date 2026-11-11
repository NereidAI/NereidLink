// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// jsonEditor.ts holds pure helpers behind the structured ⇄ JSON connector
// editors: shape parsing (config object / mappings object-array with a
// string "key"), JSON-Schema validation with optional override relaxation,
// and the set of connector types whose config follows the EdgeX device
// contract understood by NereidEdge.

import Ajv2020 from "ajv/dist/2020";

// EDGE_CONNECTOR_TYPES are the type aliases NereidEdge treats as an
// EdgeX device connector; their config carries ds_name/profile_name/
// device_name (see NereidEdge internal/connector/applier.go).
export const EDGE_CONNECTOR_TYPES = new Set([
  "nereid-edge",
  "edge-connector",
  "edgex-device",
  "edgex-connector",
]);

// isEdgeConnectorType reports whether type denotes an EdgeX connector.
export function isEdgeConnectorType(type?: string): boolean {
  return !!type && EDGE_CONNECTOR_TYPES.has(type);
}

// JsonResult is the outcome of parsing editor text.
export interface JsonResult<T> {
  ok: boolean;
  value?: T;
  error?: string;
}

// parseConfigObject parses text into a JSON object. Empty text means absent
// (represented as {} in the editor).
export function parseConfigObject(text: string): JsonResult<Record<string, unknown>> {
  const trimmed = text.trim();
  if (!trimmed) return { ok: true, value: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (err) {
    return { ok: false, error: (err as Error).message };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, error: "config must be a JSON object" };
  }
  return { ok: true, value: parsed as Record<string, unknown> };
}

// parseMappings parses text into an array of objects each carrying a
// non-empty string "key". Empty text means an empty list.
//
// In lenient mode the non-empty-key check is skipped: the structured form
// must be able to render a freshly added row (key still "") so the operator
// can fill it in. Save paths always call the strict form, so an empty key
// can never be persisted.
export function parseMappings(
  text: string,
  opts?: { lenient?: boolean },
): JsonResult<Record<string, unknown>[]> {
  const lenient = opts?.lenient ?? false;
  const trimmed = text.trim();
  if (!trimmed) return { ok: true, value: [] };
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (err) {
    return { ok: false, error: (err as Error).message };
  }
  if (!Array.isArray(parsed)) {
    return { ok: false, error: "mappings must be a JSON array" };
  }
  const entries = parsed as unknown[];
  for (let i = 0; i < entries.length; i += 1) {
    const entry = entries[i];
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      return { ok: false, error: `mappings[${i}] must be a JSON object` };
    }
    if (lenient) continue;
    const key = (entry as Record<string, unknown>).key;
    if (typeof key !== "string" || key === "") {
      return { ok: false, error: `mappings[${i}] is missing a non-empty string "key"` };
    }
  }
  return { ok: true, value: entries as Record<string, unknown>[] };
}

// cloneSchema deep-codes a JSON Schema document.
function cloneSchema(schema: Record<string, unknown>): Record<string, unknown> {
  return JSON.parse(JSON.stringify(schema)) as Record<string, unknown>;
}

// relaxSchema removes presence constraints ("required", "dependentRequired",
// "minProperties") recursively so a per-instance override document validates
// without carrying the template's required keys. It mirrors the backend's
// relaxForPatch.
function relaxSchema(node: unknown): void {
  if (Array.isArray(node)) {
    node.forEach(relaxSchema);
    return;
  }
  if (typeof node !== "object" || node === null) return;
  const obj = node as Record<string, unknown>;
  delete obj.required;
  delete obj.dependentRequired;
  delete obj.minProperties;
  for (const [key, val] of Object.entries(obj)) {
    if (key === "properties" || key === "patternProperties" || key === "$defs" || key === "defs") {
      if (val && typeof val === "object") {
        Object.values(val as Record<string, unknown>).forEach(relaxSchema);
      }
    } else if (
      key === "items" || key === "contains" || key === "additionalProperties" ||
      key === "unevaluatedProperties" || key === "not" || key === "if" || key === "then" ||
      key === "else" || key === "allOf" || key === "anyOf" || key === "oneOf" ||
      key === "prefixItems"
    ) {
      relaxSchema(val);
    }
  }
}

const ajv = new Ajv2020({ allErrors: true, strict: false });
type CompiledValidator = ReturnType<typeof ajv.compile>;

// validateAgainstSchema validates a parsed document against a draft 2020-12
// JSON Schema. When partial is true the schema's presence constraints are
// relaxed for instance-override documents. A null schema skips validation.
export function validateAgainstSchema(
  doc: unknown,
  schema: Record<string, unknown> | null,
  partial: boolean,
): string | null {
  if (!schema) return null;
  const effective = partial ? (() => {
    const cloned = cloneSchema(schema);
    relaxSchema(cloned);
    return cloned;
  })() : schema;
  let validate: CompiledValidator;
  try {
    validate = ajv.compile(effective);
  } catch (err) {
    return `invalid registered schema: ${(err as Error).message}`;
  }
  if (validate(doc)) return null;
  return (validate.errors ?? [])
    .map((e) => `${e.instancePath || "/"} ${e.message ?? "is invalid"}`)
    .join("; ");
}

// isPlainObject reports whether value is a JSON object (not array/null).
function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// mergeConfigObject deep-merges override onto template for the live
// "effective config" preview. It mirrors internal/connector.merge.go: nested
// objects merge key by key while arrays and scalars in override replace the
// template value wholesale; arrays are never concatenated. Inputs remain
// unmutated.
export function mergeConfigObject(
  template: unknown,
  override: unknown,
): Record<string, unknown> {
  if (!isPlainObject(template)) {
    return isPlainObject(override) ? structuredCloneSafe(override) : {};
  }
  if (!isPlainObject(override)) {
    return structuredCloneSafe(template);
  }
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(template)) out[k] = clone(v);
  for (const [k, ov] of Object.entries(override)) {
    const bv = out[k];
    out[k] = isPlainObject(bv) && isPlainObject(ov)
      ? mergeConfigObject(bv, ov)
      : clone(ov);
  }
  return out;
}

// effectiveMappings resolves the effective mappings list for preview: a
// non-empty instance list replaces the template list entirely (arrays are not
// concatenated), mirroring mergeMappings.
export function effectiveMappings(
  template: unknown,
  override: unknown,
): unknown[] {
  if (Array.isArray(override) && override.length > 0) return clone(override);
  if (Array.isArray(template)) return clone(template);
  return [];
}

// clone deep-copies JSON-compatible values.
function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

// structuredCloneSafe deep-copies a plain JSON object.
function structuredCloneSafe(value: Record<string, unknown>): Record<string, unknown> {
  return clone(value);
}
