// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RuleTraceDrawer is the live debugging console for a rule chain
// (ADR-0028). It toggles server-side tracing for the currently active
// chain version, polls recent per-message traces, and shows the node path
// each real message took with durations, taken relations and errors.
// Selecting a trace highlights the path on the canvas.

import { useEffect, useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Drawer,
  Empty,
  List,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { formatDateTime } from "../../utils/format";
import {
  getChainTraces,
  setChainTrace,
  type ChainTrace,
  type NodeTraceRecord,
  type RuleMsg,
} from "../../api/ruleChains";
import type { RuleNodeType } from "./RuleNode";

interface RuleTraceDrawerProps {
  // open controls visibility.
  open: boolean;

  // onClose hides the drawer; tracing keeps running on the server.
  onClose: () => void;

  // chainId is the currently active version; empty when never activated.
  chainId: string;

  // nodes are the canvas nodes, used to resolve node names.
  nodes: RuleNodeType[];

  // onApplyTrace reports the visited/error node ids to highlight on canvas.
  onApplyTrace: (visited: string[], errors: string[]) => void;

  // onFocusNode centers a node on the canvas.
  onFocusNode: (nodeId: string) => void;
}

// RuleTraceDrawer renders the bottom debugging drawer.
export default function RuleTraceDrawer({
  open,
  onClose,
  chainId,
  nodes,
  onApplyTrace,
  onFocusNode,
}: RuleTraceDrawerProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [tracing, setTracing] = useState(false);
  const [selectedTraceId, setSelectedTraceId] = useState<string>();

  // nodeName maps a node id to its display name on the canvas.
  const nodeName = useMemo(() => {
    const map = new Map<string, string>();
    nodes.forEach((node) => map.set(node.id, String(node.data.label)));
    return map;
  }, [nodes]);

  const tracesQuery = useQuery({
    queryKey: ["chain-traces", chainId],
    queryFn: () => getChainTraces(chainId),
    enabled: open && !!chainId,
    refetchInterval: tracing ? 3000 : false,
  });
  // Memoized so the auto-select effect keeps a stable dependency identity
  // instead of seeing a fresh [] fallback on every render.
  const traces = useMemo(() => tracesQuery.data ?? [], [tracesQuery.data]);

  // Select the newest trace automatically on first data, and when the
  // selected one disappears. Adjusting state during render against the
  // previously seen array is the React-recommended replacement for an
  // effect whose only job was a post-paint setState.
  const [lastTraces, setLastTraces] = useState(traces);
  if (traces !== lastTraces) {
    setLastTraces(traces);
    if (
      traces.length > 0 &&
      (!selectedTraceId ||
        !traces.some((tr) => tr.trace_id === selectedTraceId))
    ) {
      setSelectedTraceId(traces[0].trace_id);
    }
  }

  const selected = traces.find((tr) => tr.trace_id === selectedTraceId);

  // Reflect the selected trace onto the canvas highlight.
  useEffect(() => {
    if (!selected) {
      return;
    }
    const visited: string[] = [];
    const errors: string[] = [];
    selected.records.forEach((record) => {
      visited.push(record.node_id);
      if (record.error) errors.push(record.node_id);
    });
    onApplyTrace(visited, errors);
  }, [selected, onApplyTrace]);

  const mutation = useMutation({
    mutationFn: (enabled: boolean) => setChainTrace(chainId, enabled),
    onSuccess: async (_data, enabled) => {
      setTracing(enabled);
      await queryClient.invalidateQueries({ queryKey: ["chain-traces", chainId] });
      message.success(
        enabled ? t("rule.trace.started") : t("rule.trace.stopped"),
      );
    },
    onError: (error) => message.error(String(error)),
  });

  return (
    <Drawer
      open={open}
      placement="bottom"
      height={520}
      title={t("rule.trace.title")}
      onClose={onClose}
      destroyOnHidden
      styles={{ body: { padding: 0 } }}
      extra={
        <Space>
          {!chainId && (
            <Tag color="warning">{t("rule.trace.noActive")}</Tag>
          )}
          <Typography.Text type="secondary">
            {t("rule.trace.capture")}
          </Typography.Text>
          <Switch
            checked={tracing}
            disabled={!chainId || mutation.isPending}
            onChange={(checked) => mutation.mutate(checked)}
          />
          <Tooltip title={t("common.refresh")}>
            <Button
              type="text"
              icon={<ReloadOutlined />}
              disabled={!chainId}
              onClick={() => void tracesQuery.refetch()}
            />
          </Tooltip>
        </Space>
      }
    >
      <div style={{ display: "flex", height: "100%" }}>
        <div style={{ width: 300, borderRight: "1px solid #f0f0f0", overflow: "auto" }}>
          <List
            dataSource={traces}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
            renderItem={(trace: ChainTrace) => {
              const errors = trace.records.filter((r) => r.error).length;
              const active = trace.trace_id === selectedTraceId;
              return (
                <List.Item
                  onClick={() => setSelectedTraceId(trace.trace_id)}
                  style={{
                    cursor: "pointer",
                    padding: "10px 16px",
                    background: active ? "#e6f4ff" : undefined,
                  }}
                >
                  <List.Item.Meta
                    title={
                      <Space size={4}>
                        {errors > 0 ? (
                          <CloseCircleOutlined style={{ color: "#ff4d4f" }} />
                        ) : (
                          <CheckCircleOutlined style={{ color: "#52c41a" }} />
                        )}
                        <Typography.Text style={{ fontSize: 12 }} code>
                          {trace.trace_id.slice(0, 8)}
                        </Typography.Text>
                      </Space>
                    }
                    description={
                      <Space direction="vertical" size={0}>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {formatDateTime(trace.started)}
                        </Typography.Text>
                        <Space size={4}>
                          <Tag>{t("rule.trace.nodeCount", { count: trace.records.length })}</Tag>
                          {errors > 0 && (
                            <Tag color="red">{t("rule.trace.errorCount", { count: errors })}</Tag>
                          )}
                        </Space>
                      </Space>
                    }
                  />
                </List.Item>
              );
            }}
          />
        </div>

        <div style={{ flex: 1, overflow: "auto", padding: "12px 16px", background: "#fafafa" }}>
          {!selected ? (
            <Empty style={{ marginTop: 100 }} />
          ) : (
            <Space direction="vertical" size={10} style={{ display: "flex" }}>
              {selected.records.map((record, index) => (
                <NodeStep
                  key={`${record.node_id}-${index}`}
                  record={record}
                  index={index}
                  name={nodeName.get(record.node_id)}
                  onFocus={() => onFocusNode(record.node_id)}
                />
              ))}
            </Space>
          )}
        </div>
      </div>
    </Drawer>
  );
}

// codeBlockStyle is the compact JSON viewer used for message content.
const codeBlockStyle: React.CSSProperties = {
  margin: 0,
  padding: "6px 8px",
  background: "#f5f5f5",
  border: "1px solid #eee",
  borderRadius: 4,
  fontSize: 12,
  lineHeight: 1.5,
  maxHeight: 150,
  overflow: "auto",
  whiteSpace: "pre-wrap",
  wordBreak: "break-word",
};

interface NodeStepProps {
  // record is the node execution record.
  record: NodeTraceRecord;
  // index is the execution order.
  index: number;
  // name is the node display name.
  name?: string;
  // onFocus centers the node on the canvas.
  onFocus: () => void;
}

// NodeStep renders one node execution with its inbound/outbound message
// content, timing, taken relations and any error.
function NodeStep({ record, index, name, onFocus }: NodeStepProps) {
  const { t } = useTranslation();
  const failed = !!record.error;
  const accent = failed ? "#ff4d4f" : "#1677ff";

  return (
    <div
      style={{
        border: "1px solid #f0f0f0",
        borderLeft: `4px solid ${accent}`,
        borderRadius: 6,
        background: "#fff",
        padding: "8px 12px",
      }}
    >
      <Space wrap size={6} style={{ marginBottom: 6 }}>
        <Tag color={failed ? "red" : "blue"} style={{ margin: 0 }}>{index + 1}</Tag>
        <Button
          type="link"
          style={{ padding: 0, height: "auto", fontWeight: 600 }}
          onClick={onFocus}
        >
          {name ?? record.node_id.slice(0, 8)}
        </Button>
        <Tag style={{ margin: 0 }}>{record.node_type}</Tag>
        {typeof record.duration_ms === "number" && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {record.duration_ms.toFixed(2)} ms
          </Typography.Text>
        )}
        {(record.outputs ?? []).map((relation) => (
          <Tag key={relation} color="purple" style={{ margin: 0 }}>{relation}</Tag>
        ))}
      </Space>

      <div style={{ display: "flex", gap: 10, flexWrap: "wrap" }}>
        <div style={{ flex: 1, minWidth: 240 }}>
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {t("rule.trace.inbound")}
          </Typography.Text>
          <pre style={codeBlockStyle}>{stringifyMsg(record.in)}</pre>
        </div>
        {(record.out ?? []).length > 0 && (
          <div style={{ flex: 1, minWidth: 240 }}>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              {t("rule.trace.outbound")}
            </Typography.Text>
            <Space direction="vertical" size={4} style={{ display: "flex" }}>
              {(record.out ?? []).map((branch, i) => (
                <div key={`${branch.relation}-${i}`}>
                  <Tag color="purple" style={{ marginBottom: 2 }}>{branch.relation}</Tag>
                  <pre style={codeBlockStyle}>{stringifyMsg(branch.msg)}</pre>
                </div>
              ))}
            </Space>
          </div>
        )}
      </div>

      {failed && (
        <div
          style={{
            marginTop: 8,
            padding: "4px 8px",
            background: "#fff2f0",
            border: "1px solid #ffccc7",
            borderRadius: 4,
          }}
        >
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            {record.error}
          </Typography.Text>
        </div>
      )}
    </div>
  );
}

// stringifyMsg pretty-prints a rule message for the trace viewer. It keeps
// the envelope fields while making the body the focus.
function stringifyMsg(msg: RuleMsg | undefined): string {
  if (!msg) return "";
  return JSON.stringify(msg, null, 2);
}