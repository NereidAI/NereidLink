// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RuleNode is the custom React Flow node for rule chains. It renders a
// target handle, a colored header with node name/type and one source
// handle per output relation. Clicking the body opens the config drawer.

import { memo } from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { useTranslation } from "react-i18next";

import {
  catalogByType,
  nodeLabelKey,
  relationLabelKey,
  relationsFor,
  type NodeConfig,
} from "./nodeCatalog";

// RuleNodeData is the custom node payload.
export interface RuleNodeData extends Record<string, unknown> {
  // label is the human-readable node name.
  label: string;

  // nodeType is the wire node type.
  nodeType: string;

  // config holds the node configuration.
  config: NodeConfig;

  // debug marks the node as visited by the last test delivery.
  debug?: boolean;

  // debugError marks a visited node that returned an error.
  debugError?: boolean;
}

// RuleNodeType is the typed custom node.
export type RuleNodeType = Node<RuleNodeData, "ruleNode">;

// HANDLE_GAP offsets stacked relation handles vertically.
const HANDLE_GAP = 18;

// RuleNode renders the custom node body.
function RuleNode({ data, selected }: NodeProps<RuleNodeType>) {
  const { t } = useTranslation();
  const catalog = catalogByType[data.nodeType];
  const color = catalog?.color ?? "#8c8c8c";
  const relations = relationsFor(data.nodeType, data.config);

  return (
    <div
      style={{
        width: 180,
        position: "relative",
        borderRadius: 8,
        background: "#fff",
        border: `2px solid ${data.debugError ? "#ff4d4f" : selected ? color : "#d9d9d9"}`,
        boxShadow: data.debug
          ? `0 0 0 3px ${data.debugError ? "rgba(255,77,79,.25)" : "rgba(82,196,26,.35)"}`
          : undefined,
        overflow: "hidden",
      }}
    >
      <Handle
        type="target"
        position={Position.Left}
        style={{ background: color }}
      />
      <div
        style={{
          background: color,
          color: "#fff",
          padding: "6px 10px",
          fontSize: 12,
          fontWeight: 600,
        }}
      >
        {t(nodeLabelKey[data.nodeType] ?? data.nodeType)}
      </div>
      <div style={{ padding: "6px 10px", fontSize: 13 }}>{data.label}</div>

      {relations.map((relation, index) => {
        const top = 24 + index * HANDLE_GAP;
        return (
          <div key={relation}>
            <span
              style={{
                position: "absolute",
                right: 10,
                top: top + 2,
                fontSize: 10,
                color: "#8c8c8c",
                pointerEvents: "none",
              }}
            >
              {t(relationLabelKey[relation] ?? relation)}
            </span>
            <Handle
              id={relation}
              type="source"
              position={Position.Right}
              style={{ top, background: color }}
            />
          </div>
        );
      })}
    </div>
  );
}

export default memo(RuleNode);
