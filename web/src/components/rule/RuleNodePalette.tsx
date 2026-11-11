// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RuleNodePalette renders the draggable list of node types. Drag starts a
// text/uri-list transfer carrying the node type, consumed by the canvas
// drop handler.

import { Typography } from "antd";
import { useTranslation } from "react-i18next";

import { nodeCatalog, nodeLabelKey } from "./nodeCatalog";

// onDragNode begins a drag transfer for one node type.
function onDragNode(event: React.DragEvent, nodeType: string) {
  event.dataTransfer.setData("application/x-rule-node", nodeType);
  event.dataTransfer.effectAllowed = "move";
}

// RuleNodePalette renders the palette panel.
export default function RuleNodePalette() {
  const { t } = useTranslation();
  return (
    <div
      style={{
        width: 200,
        background: "#fff",
        borderRight: "1px solid #f0f0f0",
        padding: 12,
        height: "100%",
        overflow: "auto",
      }}
    >
      <Typography.Text strong style={{ display: "block", marginBottom: 8 }}>
        {t("rule.palette")}
      </Typography.Text>
      {nodeCatalog.map((entry) => (
        <div
          key={entry.type}
          draggable
          onDragStart={(event) => onDragNode(event, entry.type)}
          style={{
            border: "1px solid #f0f0f0",
            borderLeft: `4px solid ${entry.color}`,
            borderRadius: 6,
            padding: "6px 10px",
            marginBottom: 6,
            fontSize: 13,
            cursor: "grab",
            userSelect: "none",
          }}
        >
          {t(nodeLabelKey[entry.type] ?? entry.type)}
        </div>
      ))}
    </div>
  );
}
