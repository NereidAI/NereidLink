// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// businessModel.ts owns pure business-assembly rules shared by the canvas and
// future regression tests. The allowed edge matrix expresses only business
// subscriptions and triggers; it deliberately cannot represent NI frame
// topology, arbitrary DAG execution, scripts, or expressions.

import type {
  Assembly,
  BusinessInstanceCreate,
  BusinessNodeKind,
  BusinessRelation,
  Template,
} from "../../api/business";
import type { CompanionEndpoint } from "../../api/companions";
import type { GatewayModel } from "../../api/gatewayModels";

// BUSINESS_NODE_KINDS is the palette order and closed node vocabulary.
export const BUSINESS_NODE_KINDS: BusinessNodeKind[] = [
  "camera",
  "pipeline",
  "model",
  "companion",
  "rule",
  "action",
  "northbound",
  "external_servable",
];

// BUSINESS_NODE_COLORS provides a stable visual distinction between asset
// kinds without encoding runtime health as a fabricated color.
export const BUSINESS_NODE_COLORS: Record<BusinessNodeKind, string> = {
  camera: "#1677ff",
  pipeline: "#722ed1",
  model: "#13c2c2",
  companion: "#eb2f96",
  rule: "#fa8c16",
  action: "#f5222d",
  northbound: "#2f54eb",
  external_servable: "#08979c",
};

// BUSINESS_DRAG_MIME isolates native palette drags from unrelated page drops.
export const BUSINESS_DRAG_MIME = "application/x-nereidlink-business-node";

// ConnectionCandidate is the endpoint subset supplied by React Flow.
export interface ConnectionCandidate {
  source: string | null;
  target: string | null;
}

// ConnectionRejection is a stable, localizable connection failure code.
export type ConnectionRejection =
  | "missingEndpoints"
  | "unknownNode"
  | "selfConnection"
  | "duplicate"
  | "unsupportedRelation"
  | "cycle";

// ConnectionValidation describes either the inferred business relation or the
// reason a proposed edge must not enter the assembly.
export interface ConnectionValidation {
  valid: boolean;
  relation?: BusinessRelation;
  reason?: ConnectionRejection;
}

// RELATION_MATRIX is the complete source-kind/target-kind whitelist. Multiple
// entries for one pair are not needed: each allowed pair has exactly one
// unambiguous business meaning.
const RELATION_MATRIX: Partial<
  Record<BusinessNodeKind, Partial<Record<BusinessNodeKind, BusinessRelation>>>
> = {
  camera: { pipeline: "frames" },
  model: { pipeline: "uses" },
  external_servable: { pipeline: "serves" },
  pipeline: { companion: "facts", rule: "facts" },
  companion: { rule: "conclusions", northbound: "publishes" },
  rule: { action: "triggers", northbound: "publishes" },
};

// businessRelation returns the only legal relation for a kind pair, or
// undefined when that pair is outside the business-assembly vocabulary.
export function businessRelation(
  source: BusinessNodeKind,
  target: BusinessNodeKind,
): BusinessRelation | undefined {
  return RELATION_MATRIX[source]?.[target];
}

// introducesCycle reports whether adding source -> target creates a directed
// cycle. Existing malformed edges are traversed defensively without making
// the proposed connection legal.
function introducesCycle(assembly: Assembly, source: string, target: string): boolean {
  const adjacency = new Map<string, string[]>();
  for (const edge of assembly.edges) {
    const targets = adjacency.get(edge.source) ?? [];
    targets.push(edge.target);
    adjacency.set(edge.source, targets);
  }

  const pending = [target];
  const visited = new Set<string>();
  while (pending.length > 0) {
    const current = pending.pop()!;
    if (current === source) return true;
    if (visited.has(current)) continue;
    visited.add(current);
    pending.push(...(adjacency.get(current) ?? []));
  }
  return false;
}

// validateBusinessConnection validates endpoints, uniqueness, the closed
// relation matrix, and acyclicity. It is pure so the same rules can be tested
// without mounting React Flow.
export function validateBusinessConnection(
  assembly: Assembly,
  candidate: ConnectionCandidate,
): ConnectionValidation {
  if (!candidate.source || !candidate.target) {
    return { valid: false, reason: "missingEndpoints" };
  }
  if (candidate.source === candidate.target) {
    return { valid: false, reason: "selfConnection" };
  }

  const source = assembly.nodes.find((node) => node.id === candidate.source);
  const target = assembly.nodes.find((node) => node.id === candidate.target);
  if (!source || !target) return { valid: false, reason: "unknownNode" };

  if (
    assembly.edges.some(
      (edge) => edge.source === source.id && edge.target === target.id,
    )
  ) {
    return { valid: false, reason: "duplicate" };
  }

  const relation = businessRelation(source.kind, target.kind);
  if (!relation) return { valid: false, reason: "unsupportedRelation" };
  if (introducesCycle(assembly, source.id, target.id)) {
    return { valid: false, reason: "cycle" };
  }
  return { valid: true, relation };
}

// businessTargetGatewayIds resolves the gateways whose cameras feed pipelines
// related to one model or companion. Direct kind/relation checks deliberately
// avoid treating the whole connected business graph as one deployment target.
export function businessTargetGatewayIds(
  assembly: Assembly,
  nodeId: string,
): string[] {
  const nodes = new Map(assembly.nodes.map((node) => [node.id, node]));
  const selected = nodes.get(nodeId);
  if (!selected || (selected.kind !== "model" && selected.kind !== "companion")) {
    return [];
  }

  const pipelineIds = new Set<string>();
  for (const edge of assembly.edges) {
    if (
      selected.kind === "model" &&
      edge.source === nodeId &&
      edge.relation === "uses" &&
      nodes.get(edge.target)?.kind === "pipeline"
    ) {
      pipelineIds.add(edge.target);
    }
    if (
      selected.kind === "companion" &&
      edge.target === nodeId &&
      edge.relation === "facts" &&
      nodes.get(edge.source)?.kind === "pipeline"
    ) {
      pipelineIds.add(edge.source);
    }
  }

  const gatewayIds = new Set<string>();
  for (const edge of assembly.edges) {
    if (edge.relation !== "frames" || !pipelineIds.has(edge.target)) continue;
    const camera = nodes.get(edge.source);
    if (camera?.kind !== "camera") continue;
    const gatewayId = typeof camera.asset.gateway_id === "string"
      ? camera.asset.gateway_id.trim()
      : "";
    if (gatewayId) gatewayIds.add(gatewayId);
  }
  return [...gatewayIds].sort();
}

// intersectReadyGatewayModels returns deployable id/version identities present
// in every target inventory. A model available on only one of several gateways
// must not appear selectable for the shared business asset.
export function intersectReadyGatewayModels(
  inventories: GatewayModel[][],
): GatewayModel[] {
  if (inventories.length === 0) return [];
  const identity = (model: GatewayModel) => `${model.id}\u0000${model.version}`;
  const ready = (model: GatewayModel) =>
    model.state === "READY" && !model.stale && Boolean(model.id) && Boolean(model.version);

  const common = new Map<string, GatewayModel>();
  for (const model of inventories[0]) {
    if (ready(model) && !common.has(identity(model))) common.set(identity(model), model);
  }
  for (const inventory of inventories.slice(1)) {
    const available = new Set(inventory.filter(ready).map(identity));
    for (const key of common.keys()) {
      if (!available.has(key)) common.delete(key);
    }
  }
  return [...common.values()].sort(
    (left, right) => left.id.localeCompare(right.id) || left.version.localeCompare(right.version),
  );
}

// availableCompanionIds intersects enabled endpoint registrations by logical
// companion id. Every derived target gateway needs an endpoint because the
// expansion creates one subscription artifact per gateway.
export function availableCompanionIds(
  endpoints: CompanionEndpoint[],
  gatewayIds: string[],
): string[] {
  const targets = [...new Set(gatewayIds)].sort();
  if (targets.length === 0) return [];
  const registrations = targets.map((gatewayId) =>
    new Set(
      endpoints
        .filter((endpoint) => endpoint.enabled && endpoint.gateway_id === gatewayId)
        .map((endpoint) => endpoint.companion_id)
        .filter(Boolean),
    ),
  );
  return [...registrations[0]]
    .filter((companionId) => registrations.every((set) => set.has(companionId)))
    .sort();
}

// clearChangedGatewayAssets removes dependent references when a camera or
// relationship edit changes a model/companion's deployment targets. Unrelated
// asset settings and conclusion-device ownership remain untouched.
export function clearChangedGatewayAssets(
  previous: Assembly,
  next: Assembly,
): Assembly {
  const previousNodes = new Map(previous.nodes.map((node) => [node.id, node]));
  let reconciled: Assembly | undefined;

  next.nodes.forEach((node, index) => {
    if (node.kind !== "model" && node.kind !== "companion") return;
    if (!previousNodes.has(node.id)) return;
    const before = businessTargetGatewayIds(previous, node.id).join("\u0000");
    const after = businessTargetGatewayIds(next, node.id).join("\u0000");
    if (before === after) return;

    const fields = node.kind === "model"
      ? ["model_id", "version"]
      : ["companion_id"];
    if (!fields.some((field) => Object.prototype.hasOwnProperty.call(node.asset, field))) return;
    if (!reconciled) reconciled = { ...next, nodes: [...next.nodes] };
    const asset = { ...node.asset };
    for (const field of fields) delete asset[field];
    reconciled.nodes[index] = { ...node, asset };
  });

  return reconciled ?? next;
}

// cloneJSON copies template JSON data so editing a draft can never mutate the
// immutable template object held by the query cache.
function cloneJSON<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

// parameterDefaults extracts only flat top-level JSON Schema defaults. Nested
// camera, model, and other asset documents stay in their dedicated inspectors
// and are never passed through SchemaFields as pseudo-parameter forms.
function parameterDefaults(schema: Record<string, unknown>): Record<string, unknown> {
  const properties = schema.properties;
  if (!properties || typeof properties !== "object" || Array.isArray(properties)) {
    return {};
  }

  const values: Record<string, unknown> = {};
  for (const [key, rawProperty] of Object.entries(properties)) {
    if (!rawProperty || typeof rawProperty !== "object" || Array.isArray(rawProperty)) {
      continue;
    }
    const property = rawProperty as Record<string, unknown>;
    if (Object.prototype.hasOwnProperty.call(property, "default")) {
      values[key] = cloneJSON(property.default);
    }
  }
  return values;
}

// templateToDraft converts an immutable template version into an independent
// instance-create draft, preserving stable assembly IDs and cloning every
// nested asset. Flat schema defaults seed only unset parameter values.
export function templateToDraft(
  template: Template,
  name: string,
): BusinessInstanceCreate {
  return {
    name: name.trim(),
    template_id: template.id,
    template_version: template.version,
    parameters: parameterDefaults(template.parameters),
    assembly: cloneJSON(template.assembly),
  };
}

// AssemblyPreparation is either a JSON-safe assembly or the asset node whose
// raw JSON field is invalid. No server request should be made for the
// latter because it would hide a precise editor error behind a generic 400.
export interface AssemblyPreparation {
  assembly: Assembly;
  invalidNodeId?: string;
}

// parseObjectField converts one raw editor string to a JSON object, removes a
// blank optional value, and leaves an already structured value untouched.
function parseObjectField(container: Record<string, unknown>, field: string): boolean {
  const rawValue = container[field];
  if (typeof rawValue !== "string") return true;
  if (!rawValue.trim()) {
    delete container[field];
    return true;
  }
  try {
    const parsed = JSON.parse(rawValue) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return false;
    container[field] = parsed as Record<string, unknown>;
    return true;
  } catch {
    return false;
  }
}

// prepareAssemblyForSave clones editor state and parses raw JSON fields into
// their wire representations. Empty optional definitions are omitted; all
// other scalar deployment fields pass through without reinterpretation.
export function prepareAssemblyForSave(assembly: Assembly): AssemblyPreparation {
  const prepared = cloneJSON(assembly);
  for (const node of prepared.nodes) {
    if (node.kind === "pipeline" && !parseObjectField(node.asset, "definition")) {
      return { assembly: prepared, invalidNodeId: node.id };
    }
    if (node.kind === "external_servable") {
      if (!parseObjectField(node.asset, "definition")) {
        return { assembly: prepared, invalidNodeId: node.id };
      }
      const definition = node.asset.definition;
      if (
        definition &&
        typeof definition === "object" &&
        !Array.isArray(definition) &&
        !parseObjectField(definition as Record<string, unknown>, "descriptor")
      ) {
        return { assembly: prepared, invalidNodeId: node.id };
      }
    }
  }
  return { assembly: prepared };
}
