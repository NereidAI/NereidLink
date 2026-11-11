// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodeCatalog.ts declares the rule node types available in the editor:
// labels, colors, default configuration and the output relations each
// type exposes. It mirrors the node type identifiers in internal/rule.

import type { SwitchCase } from "../../api/ruleChains";

// NodeConfig is the union-ish configuration shape. Fields are optional
// because each node type reads only its own subset.
export interface NodeConfig {
  script?: string;
  message?: string;
  field?: string;
  scope?: string;
  alarm_type?: string;
  severity?: string;
  dedupe_key_script?: string;
  details_script?: string;
  propagate?: boolean;
  escalation_chain_id?: string;
  command_name?: string;
  params_script?: string;
  timeout_ms?: number;
  forwarder_id?: string;
  cases?: SwitchCase[];
  has_default?: boolean;
}

// CatalogEntry describes one draggable node type.
export interface CatalogEntry {
  // Type is the wire node type identifier.
  type: string;

  // Color groups the node visually.
  color: string;

  // DefaultConfig seeds a newly dropped node.
  defaultConfig: NodeConfig;

  // Relations lists the output relations; for dynamic nodes (switch) it
  // is resolved at render time from node data.
  relations?: string[];
}

// NodeLabelKey is the locale path of a node type display name.
export type NodeLabelKey =
  | "rule.nodes.scriptFilter"
  | "rule.nodes.switch"
  | "rule.nodes.transform"
  | "rule.nodes.saveAttributes"
  | "rule.nodes.createAlarm"
  | "rule.nodes.clearAlarm"
  | "rule.nodes.sendCommand"
  | "rule.nodes.forward"
  | "rule.nodes.log"
  | "rule.nodes.delimiter";

// nodeLabelKey maps each wire node type to its locale label key.
export const nodeLabelKey: Record<string, NodeLabelKey> = {
  "script-filter": "rule.nodes.scriptFilter",
  switch: "rule.nodes.switch",
  transform: "rule.nodes.transform",
  "save-attributes": "rule.nodes.saveAttributes",
  "create-alarm": "rule.nodes.createAlarm",
  "clear-alarm": "rule.nodes.clearAlarm",
  "send-command": "rule.nodes.sendCommand",
  forward: "rule.nodes.forward",
  log: "rule.nodes.log",
  delimiter: "rule.nodes.delimiter",
};

// NodeRelationKey is the locale path of a static output relation label.
export type NodeRelationKey =
  | "rule.relations.true"
  | "rule.relations.false"
  | "rule.relations.success"
  | "rule.relations.failure"
  | "rule.relations.default";

// relationLabelKey maps each wire relation identifier to its locale label key.
export const relationLabelKey: Record<string, NodeRelationKey> = {
  True: "rule.relations.true",
  False: "rule.relations.false",
  Success: "rule.relations.success",
  Failure: "rule.relations.failure",
  Default: "rule.relations.default",
};

// nodeCatalog lists every supported node type.
export const nodeCatalog: CatalogEntry[] = [
  {
    type: "script-filter",
    color: "#1677ff",
    defaultConfig: { script: "return msg.temperature > 30" },
    relations: ["True", "False"],
  },
  {
    type: "switch",
    color: "#722ed1",
    defaultConfig: {
      cases: [
        { label: "Hot", script: "return msg.temperature > 30" },
      ],
      has_default: true,
    },
  },
  {
    type: "transform",
    color: "#13c2c2",
    defaultConfig: { script: "return { ...msg, f: msg.temperature * 9 / 5 + 32 }" },
    relations: ["Success"],
  },
  {
    type: "save-attributes",
    color: "#52c41a",
    defaultConfig: { scope: "CLIENT", script: "({ seen: true })" },
    relations: ["Success"],
  },
  {
    type: "create-alarm",
    color: "#fa541c",
    defaultConfig: {
      alarm_type: "HighTemperature",
      severity: "MAJOR",
      dedupe_key_script: "",
      details_script: "",
      propagate: false,
    },
    relations: ["Success"],
  },
  {
    type: "clear-alarm",
    color: "#a0d911",
    defaultConfig: { alarm_type: "HighTemperature", dedupe_key_script: "" },
    relations: ["Success"],
  },
  {
    type: "send-command",
    color: "#eb2f96",
    defaultConfig: {
      command_name: "reboot",
      params_script: "",
      timeout_ms: 0,
    },
    relations: ["Success", "Failure"],
  },
  {
    type: "log",
    color: "#595959",
    defaultConfig: { message: "message received" },
    relations: ["Success"],
  },
  {
    type: "forward",
    color: "#2f54eb",
    defaultConfig: { forwarder_id: "" },
    relations: ["Success"],
  },
  {
    type: "delimiter",
    color: "#fa8c16",
    defaultConfig: { field: "items" },
    relations: ["Success", "Failure"],
  },
];

// catalogByType indexes catalog entries by wire type.
export const catalogByType: Record<string, CatalogEntry> = Object.fromEntries(
  nodeCatalog.map((entry) => [entry.type, entry]),
);

// relationsFor returns the output relation handles for one node. Switch
// handles are dynamic, one per configured case plus the default relation.
export function relationsFor(nodeType: string, config: NodeConfig): string[] {
  if (nodeType === "switch") {
    const relations = (config.cases ?? []).map((c) => c.label);
    if (config.has_default) relations.push("Default");
    return relations;
  }
  return catalogByType[nodeType]?.relations ?? ["Success"];
}
