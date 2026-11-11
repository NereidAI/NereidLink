// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BusinessFlowNode renders one asset reference with generic source/target
// handles. The handle pair is intentionally not a frame pipeline: React Flow
// connections are checked against the business relation matrix before save.

import { memo } from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { Badge, Tooltip, Typography } from "antd";
import { ExclamationCircleFilled } from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import type { BusinessNodeKind, ValidationIssue } from "../../api/business";
import { BUSINESS_NODE_COLORS } from "./businessModel";

// BusinessFlowNodeData is the editable wire node plus transient presentation
// data. Validation issues arrive from the server and are never inferred from
// missing monitoring metrics.
export interface BusinessFlowNodeData extends Record<string, unknown> {
  kind: BusinessNodeKind;
  name: string;
  ref: string;
  asset: Record<string, unknown>;
  issues: ValidationIssue[];
}

// BusinessFlowNodeType is the typed React Flow node used by the editor.
export type BusinessFlowNodeType = Node<BusinessFlowNodeData, "businessNode">;

// BusinessFlowNode renders the colored asset body and inline issue marker.
function BusinessFlowNode({ data, selected }: NodeProps<BusinessFlowNodeType>) {
  const { t } = useTranslation();
  const color = BUSINESS_NODE_COLORS[data.kind];
  const blocking = data.issues.some((issue) => issue.blocking);

  return (
    <div
      aria-label={`${t(`business.kinds.${data.kind}`)}: ${data.name}`}
      style={{
        width: 190,
        overflow: "hidden",
        borderRadius: 8,
        border: `2px solid ${selected ? color : blocking ? "#ff4d4f" : "#d9d9d9"}`,
        background: "#fff",
        boxShadow: selected ? `0 0 0 3px ${color}22` : undefined,
      }}
    >
      <Handle type="target" position={Position.Left} style={{ background: color }} />
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 8,
          padding: "6px 10px",
          color: "#fff",
          background: color,
          fontSize: 12,
          fontWeight: 600,
        }}
      >
        <span>{t(`business.kinds.${data.kind}`)}</span>
        {data.issues.length > 0 && (
          <Tooltip
            title={
              <div>
                {data.issues.map((issue, index) => (
                  <div key={`${issue.code}-${index}`}>{issue.message}</div>
                ))}
              </div>
            }
          >
            <Badge
              count={data.issues.length}
              size="small"
              color={blocking ? "#ff4d4f" : "#faad14"}
              aria-label={t("business.canvas.issueMarker", {
                count: data.issues.length,
              })}
            >
              <ExclamationCircleFilled style={{ color: "#fff" }} />
            </Badge>
          </Tooltip>
        )}
      </div>
      <div style={{ padding: "8px 10px" }}>
        <Typography.Text strong ellipsis={{ tooltip: data.name }} style={{ display: "block" }}>
          {data.name}
        </Typography.Text>
        {data.ref && (
          <Typography.Text type="secondary" ellipsis={{ tooltip: data.ref }} style={{ display: "block" }}>
            {data.ref}
          </Typography.Text>
        )}
      </div>
      <Handle type="source" position={Position.Right} style={{ background: color }} />
    </div>
  );
}

export default memo(BusinessFlowNode);
