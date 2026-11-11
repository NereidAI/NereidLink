// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// docs.ts is the help documentation registry. All bilingual Markdown
// documents are eagerly imported as raw strings, so the page Drawer can
// render synchronously and the help center can run client-side full-text
// search without additional requests.

// HelpGroup classifies documents in the help center tree.
export type HelpGroup =
  | "monitoring"
  | "devices"
  | "configuration"
  | "operations"
  | "integration"
  | "system";

// HelpEntry describes one help document.
export interface HelpEntry {
  // id is both the document identifier and the Markdown file basename.
  id: string;

  // group places the document in the help center tree.
  group: HelpGroup;

  // order sorts documents inside a group.
  order: number;
}

// helpEntries is the complete document catalog.
export const helpEntries: HelpEntry[] = [
  { id: "overview", group: "monitoring", order: 10 },
  { id: "stats", group: "monitoring", order: 20 },
  { id: "dashboards", group: "monitoring", order: 30 },
  { id: "devices", group: "devices", order: 10 },
  { id: "device-detail", group: "devices", order: 20 },
  { id: "device-shadow", group: "devices", order: 30 },
  { id: "credentials", group: "devices", order: 40 },
  { id: "quarantine", group: "devices", order: 50 },
  { id: "customers", group: "devices", order: 60 },
  { id: "connectors", group: "devices", order: 70 },
  { id: "edge-profiles", group: "devices", order: 71 },
  { id: "link-health", group: "devices", order: 72 },
  { id: "edge-models", group: "devices", order: 73 },
  { id: "profiles", group: "configuration", order: 10 },
  { id: "device-services", group: "configuration", order: 11 },
  { id: "edge-secret-types", group: "configuration", order: 12 },
  { id: "edge-secrets", group: "configuration", order: 13 },
  { id: "gateway-operations", group: "configuration", order: 14 },
  { id: "data-quality", group: "configuration", order: 15 },
  { id: "profile-delivery", group: "configuration", order: 16 },
  { id: "rule-chains", group: "configuration", order: 20 },
  { id: "business-orchestration", group: "configuration", order: 25 },
  { id: "rule-chain-editor", group: "configuration", order: 30 },
  { id: "forwarders", group: "configuration", order: 31 },
  { id: "companion-endpoints", group: "configuration", order: 32 },
  { id: "escalation-chains", group: "configuration", order: 35 },
  { id: "storage-hooks", group: "configuration", order: 40 },
  { id: "commands", group: "operations", order: 10 },
  { id: "probes", group: "operations", order: 15 },
  { id: "alarms", group: "operations", order: 20 },
  { id: "notifications", group: "operations", order: 30 },
  { id: "dlq", group: "operations", order: 40 },
  { id: "firmware", group: "operations", order: 50 },
  { id: "ota-tasks", group: "operations", order: 60 },
  { id: "integrate-direct-device", group: "integration", order: 10 },
  { id: "integrate-gateway", group: "integration", order: 20 },
  { id: "integrate-api-keys", group: "integration", order: 21 },
  { id: "system-tenants", group: "system", order: 10 },
  { id: "system-audit", group: "system", order: 20 },
  { id: "system-cas", group: "system", order: 30 },
  { id: "system-ops-orders", group: "system", order: 40 },
  { id: "system-license", group: "system", order: 50 },
  { id: "system-users", group: "system", order: 60 },
  { id: "account-security", group: "system", order: 70 },
];

// rawDocs maps "<lang>/<id>" to the raw Markdown source.
const rawDocs = import.meta.glob<string>("../docs/{en,zh}/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
});

// getDoc returns the Markdown document for the id and language. When the
// localized document is missing, English is used as the fallback.
export function getDoc(id: string, language: string): string {
  const lang = language.startsWith("zh") ? "zh" : "en";
  const key = `../docs/${lang}/${id}.md`;
  if (rawDocs[key]) return rawDocs[key];
  return rawDocs[`../docs/en/${id}.md`] ?? "";
}
