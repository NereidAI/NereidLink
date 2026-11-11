// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// business.ts is the tenant-scoped REST boundary for versioned business
// templates, generation-fenced instances, deterministic expansions, lifecycle
// actions, and on-demand health reads. It contains no canvas or frame-path
// semantics; those belong to the business document and its server-side owners.

import client from "./client";
import { currentTenantId } from "./tenant";

// BusinessNodeKind is the closed asset-reference vocabulary allowed on the
// business assembly canvas.
export type BusinessNodeKind =
  | "camera"
  | "pipeline"
  | "model"
  | "companion"
  | "rule"
  | "action"
  | "northbound"
  | "external_servable";

// BusinessRelation is a subscription, dependency, publication, or trigger
// relation. It never describes per-frame pipeline topology.
export type BusinessRelation =
  | "frames"
  | "uses"
  | "serves"
  | "facts"
  | "conclusions"
  | "triggers"
  | "publishes";

// BusinessPosition is a persisted canvas coordinate.
export interface BusinessPosition {
  x: number;
  y: number;
}

// BusinessNorthboundAsset carries the NL forwarder selection and the explicit
// raw EdgeX names used by NE's gateway-level northbound filter. These names are
// not derived from NereidLink device UUIDs or canonical device names.
export interface BusinessNorthboundAsset {
  channel?: string;
  forwarder_id?: string;
  readings?: string[];
  edgex_device_name?: string;
  edgex_profile_name?: string;
  edgex_resource_names?: string[];
  [key: string]: unknown;
}

// BusinessNode is one stable asset reference in a business assembly.
export interface BusinessNode {
  id: string;
  kind: BusinessNodeKind;
  name: string;
  ref: string;
  asset: Record<string, unknown>;
  position: BusinessPosition;
}

// BusinessEdge connects two assembly nodes with a closed business relation.
export interface BusinessEdge {
  id: string;
  source: string;
  target: string;
  relation: BusinessRelation;
}

// Assembly is the complete business-intent graph stored on templates and
// instances. Node IDs remain stable across generations.
export interface Assembly {
  nodes: BusinessNode[];
  edges: BusinessEdge[];
}

// Template is one immutable, importable business-template version. The
// platform ships no business vocabulary of its own; versions only exist after
// a pack import or a designer publish.
export interface Template {
  id: string;
  version: string;
  name: string;
  description: string;
  schema_version: string;
  requires: Record<string, unknown>;
  parameters: Record<string, unknown>;
  assembly: Assembly;
}

// ValidationIssue identifies a document or live-preflight problem. Blocking
// issues prevent deployment; node_id and artifact_key locate the source when
// the issue is specific to one item.
export interface ValidationIssue {
  code: string;
  message: string;
  node_id?: string;
  artifact_key?: string;
  blocking: boolean;
}

// ValidationReport is the latest server-authoritative validation result.
export interface ValidationReport {
  valid: boolean;
  issues: ValidationIssue[];
}

// BusinessInstance is one generation-fenced deployment of a template
// snapshot. active_generation can remain behind generation during a failed or
// in-progress rollout.
export interface BusinessInstance {
  id: string;
  name: string;
  template_id: string;
  template_version: string;
  status: string;
  generation: number;
  active_generation: number;
  pending_generation: number;
  parameters: Record<string, unknown>;
  assembly: Assembly;
  validation?: ValidationReport;
  created_at: string;
  updated_at: string;
  last_error?: string;
}

// BusinessInstanceCreate is the payload used to instantiate a template.
export interface BusinessInstanceCreate {
  name: string;
  template_id: string;
  template_version: string;
  parameters: Record<string, unknown>;
  assembly: Assembly;
}

// BusinessInstanceUpdate is the optimistic-concurrency payload for replacing
// editable instance intent.
export interface BusinessInstanceUpdate {
  name: string;
  parameters: Record<string, unknown>;
  assembly: Assembly;
  expected_generation: number;
}

// BusinessArtifactKind is the closed expansion artifact vocabulary emitted by
// the server. northbound_filter is the gateway-level NE singleton connector.
export type BusinessArtifactKind =
  | "model_distribution"
  | "pipeline_registration"
  | "external_servable"
  | "inference_binding"
  | "companion_subscription"
  | "rule_chain"
  | "northbound_channel"
  | "northbound_filter";

// ExpansionArtifact is one declarative owner-specific delivery record.
export interface ExpansionArtifact {
  key: string;
  kind: BusinessArtifactKind;
  gateway_id?: string;
  device_id?: string;
  depends_on: string[];
  spec: Record<string, unknown>;
}

// Expansion is the immutable preview of artifacts produced for one desired
// generation. Its digest is computed by the server.
export interface Expansion {
  schema_version: string;
  instance_id: string;
  generation: number;
  template: { id: string; version: string };
  digest: string;
  artifacts: ExpansionArtifact[];
}

// CameraMonitor contains the health and optional job metrics available for
// one generated camera binding. Missing metrics remain undefined so the UI
// can distinguish unavailable data from a real zero.
export interface CameraMonitor {
  node_id: string;
  device_id: string;
  gateway_id: string;
  state: string;
  last_error?: string;
  success_total?: number;
  error_total?: number;
  p50_ms?: number;
  p95_ms?: number;
  events_per_second?: number;
  health_source: string;
}

// Monitor is an on-demand health snapshot; it is not a polling subscription.
export interface Monitor {
  instance_id: string;
  generation: number;
  active_generation: number;
  status: string;
  cameras: CameraMonitor[];
  issues: ValidationIssue[];
}

// templatesBase returns the current tenant's template collection path.
function templatesBase(): string {
  return `/v1/tenants/${currentTenantId()}/business-templates`;
}

// instancesBase returns the current tenant's instance collection path.
function instancesBase(): string {
  return `/v1/tenants/${currentTenantId()}/business-instances`;
}

// instancePath returns one encoded instance resource path.
function instancePath(id: string): string {
  return `${instancesBase()}/${encodeURIComponent(id)}`;
}

// normalizeAssembly defensively restores the []-never-null collection
// invariant at the client boundary.
function normalizeAssembly(assembly: Assembly): Assembly {
  return {
    nodes: (assembly.nodes ?? []).map((node) => ({
      ...node,
      asset: node.asset ?? {},
    })),
    edges: assembly.edges ?? [],
  };
}

// normalizeInstance applies collection defaults used by editors and tables.
function normalizeInstance(instance: BusinessInstance): BusinessInstance {
  return {
    ...instance,
    parameters: instance.parameters ?? {},
    assembly: normalizeAssembly(instance.assembly),
    validation: instance.validation
      ? {
          ...instance.validation,
          issues: instance.validation.issues ?? [],
        }
      : undefined,
  };
}

// listBusinessTemplates fetches every imported template version.
export async function listBusinessTemplates(): Promise<Template[]> {
  const { data } = await client.get<Template[]>(templatesBase());
  return (data ?? []).map((template) => ({
    ...template,
    requires: template.requires ?? {},
    parameters: template.parameters ?? {},
    assembly: normalizeAssembly(template.assembly),
  }));
}

// createBusinessTemplate persists one immutable custom id+version document.
export async function createBusinessTemplate(template: Template): Promise<Template> {
  const { data } = await client.post<Template>(templatesBase(), template);
  return data;
}

// importBusinessTemplates uploads a raw YAML/JSON pack document (a bare
// template or a {"templates":[...]} envelope). The endpoint takes the pack as
// the request body rather than multipart form data.
export async function importBusinessTemplates(file: File): Promise<Template[]> {
  const body = await file.text();
  const contentType = file.name.toLowerCase().endsWith(".json")
    ? "application/json"
    : "application/yaml";
  const { data } = await client.post<Template[]>(`${templatesBase()}/import`, body, {
    headers: { "Content-Type": contentType },
  });
  return data ?? [];
}

// exportBusinessTemplates downloads the tenant's portable YAML pack.
export async function exportBusinessTemplates(): Promise<Blob> {
  const { data } = await client.get<Blob>(`${templatesBase()}/export`, {
    responseType: "blob",
  });
  return data;
}

// deleteBusinessTemplate removes one unreferenced version. The server fences
// referenced versions so historical snapshots remain valid.
export async function deleteBusinessTemplate(template: Template): Promise<void> {
  await client.delete(
    `${templatesBase()}/${encodeURIComponent(template.id)}/versions/${encodeURIComponent(template.version)}`,
  );
}

// listBusinessInstances fetches the tenant's instances without polling.
export async function listBusinessInstances(): Promise<BusinessInstance[]> {
  const { data } = await client.get<BusinessInstance[]>(instancesBase());
  return (data ?? []).map(normalizeInstance);
}

// createBusinessInstance creates the first desired generation from a draft.
export async function createBusinessInstance(
  draft: BusinessInstanceCreate,
): Promise<BusinessInstance> {
  const { data } = await client.post<BusinessInstance>(instancesBase(), draft);
  return normalizeInstance(data);
}

// getBusinessInstance reads one current instance on demand.
export async function getBusinessInstance(id: string): Promise<BusinessInstance> {
  const { data } = await client.get<BusinessInstance>(instancePath(id));
  return normalizeInstance(data);
}

// updateBusinessInstance replaces editable intent only when the expected
// generation still matches the server's desired generation.
export async function updateBusinessInstance(
  id: string,
  update: BusinessInstanceUpdate,
): Promise<BusinessInstance> {
  const { data } = await client.put<BusinessInstance>(instancePath(id), update);
  return normalizeInstance(data);
}

// deleteBusinessInstance removes an instance through the server lifecycle
// rules; the UI does not infer whether related owner resources are shared.
export async function deleteBusinessInstance(id: string): Promise<void> {
  await client.delete(instancePath(id));
}

// validateBusinessInstance runs document validation and live preflight for
// the current saved generation.
export async function validateBusinessInstance(
  id: string,
): Promise<ValidationReport> {
  const { data } = await client.post<ValidationReport>(
    `${instancePath(id)}/validate`,
  );
  return { ...data, issues: data.issues ?? [] };
}

// getBusinessExpansion reads the deterministic artifact preview for a
// generation, defaulting to the current desired generation on the server.
export async function getBusinessExpansion(
  id: string,
  generation?: number,
): Promise<Expansion> {
  const { data } = await client.get<Expansion>(`${instancePath(id)}/expansion`, {
    params: generation === undefined ? undefined : { generation },
  });
  return {
    ...data,
    artifacts: (data.artifacts ?? []).map((artifact) => ({
      ...artifact,
      depends_on: artifact.depends_on ?? [],
      spec: artifact.spec ?? {},
    })),
  };
}

// deployBusinessInstance asks the server to preflight and apply the saved
// desired generation.
export async function deployBusinessInstance(
  id: string,
): Promise<BusinessInstance> {
  const { data } = await client.post<BusinessInstance>(`${instancePath(id)}/deploy`);
  return normalizeInstance(data);
}

// undeployBusinessInstance withdraws only resources owned by this instance.
export async function undeployBusinessInstance(
  id: string,
): Promise<BusinessInstance> {
  const { data } = await client.post<BusinessInstance>(`${instancePath(id)}/undeploy`);
  return normalizeInstance(data);
}

// rollbackBusinessInstance re-applies a retained generation through the same
// server-side owners used by deployment.
export async function rollbackBusinessInstance(
  id: string,
  generation: number,
): Promise<BusinessInstance> {
  const { data } = await client.post<BusinessInstance>(`${instancePath(id)}/rollback`, {
    generation,
  });
  return normalizeInstance(data);
}

// getBusinessMonitor reads health once. Optional numeric fields are returned
// untouched so unavailable metrics never become fabricated zeros.
export async function getBusinessMonitor(id: string): Promise<Monitor> {
  const { data } = await client.get<Monitor>(`${instancePath(id)}/monitor`);
  return {
    ...data,
    cameras: data.cameras ?? [],
    issues: data.issues ?? [],
  };
}
