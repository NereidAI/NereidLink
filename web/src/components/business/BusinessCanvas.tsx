// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BusinessCanvas combines a native HTML5 asset palette, a React Flow business
// assembly, and the nested asset inspector. Its controlled boundary emits
// only Assembly documents; selection, viewport, and issue highlighting remain
// transient UI state and never leak into the saved wire format.

import { useCallback, useMemo, useState, type DragEvent } from "react";
import {
  Background,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
  type Connection,
  type Edge,
  type EdgeChange,
  type NodeChange,
  type NodeTypes,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { App as AntApp, Button, Space, Typography } from "antd";
import { DeleteOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import type {
  Assembly,
  BusinessNode,
  BusinessNodeKind,
  BusinessRelation,
  ValidationIssue,
} from "../../api/business";
import { newUUID } from "../../utils/id";
import BusinessFlowNode, {
  type BusinessFlowNodeType,
} from "./BusinessFlowNode";
import BusinessInspector from "./BusinessInspector";
import {
  BUSINESS_DRAG_MIME,
  BUSINESS_NODE_COLORS,
  BUSINESS_NODE_KINDS,
  clearChangedGatewayAssets,
  validateBusinessConnection,
} from "./businessModel";

// BusinessFlowEdgeData carries the persisted relation separately from its
// localized React Flow label.
interface BusinessFlowEdgeData extends Record<string, unknown> {
  relation: BusinessRelation;
}

// BusinessFlowEdge is the editor edge with a typed business relation.
type BusinessFlowEdge = Edge<BusinessFlowEdgeData>;

interface BusinessCanvasProps {
  assembly: Assembly;
  issues: ValidationIssue[];
  parameterSchema: Record<string, unknown>;
  parameters: Record<string, unknown>;
  showParameters?: boolean;
  onAssemblyChange: (assembly: Assembly) => void;
  onParametersChange: (parameters: Record<string, unknown>) => void;
}

// nodeTypes is stable across renders so React Flow does not remount every
// business node when an inspector value changes.
const nodeTypes: NodeTypes = { businessNode: BusinessFlowNode };

// toAssembly projects transient React Flow objects back to the exact wire
// document, dropping selection, dimensions, labels, and validation overlays.
function toAssembly(
  nodes: BusinessFlowNodeType[],
  edges: BusinessFlowEdge[],
): Assembly {
  return {
    nodes: nodes.map((node) => ({
      id: node.id,
      kind: node.data.kind,
      name: node.data.name,
      ref: node.data.ref,
      asset: node.data.asset,
      position: { x: node.position.x, y: node.position.y },
    })),
    edges: edges.map((edge) => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      relation: edge.data!.relation,
    })),
  };
}

// BusinessCanvas provides the React Flow context required by drop projection.
export default function BusinessCanvas(props: BusinessCanvasProps) {
  return (
    <ReactFlowProvider>
      <BusinessCanvasInner {...props} />
    </ReactFlowProvider>
  );
}

// BusinessCanvasInner owns transient canvas state. The parent keys this
// component by editor revision when loading another template or instance;
// ordinary edits therefore do not reset the viewport or node selection.
function BusinessCanvasInner({
  assembly,
  issues,
  parameterSchema,
  parameters,
  showParameters = true,
  onAssemblyChange,
  onParametersChange,
}: BusinessCanvasProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const reactFlow = useReactFlow();
  const [nodes, setNodes] = useState<BusinessFlowNodeType[]>(() =>
    assembly.nodes.map((node) => ({
      id: node.id,
      type: "businessNode",
      position: { ...node.position },
      data: {
        kind: node.kind,
        name: node.name,
        ref: node.ref,
        asset: node.asset,
        issues: issues.filter((issue) => issue.node_id === node.id),
      },
    })),
  );
  const [edges, setEdges] = useState<BusinessFlowEdge[]>(() =>
    assembly.edges.map((edge) => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      data: { relation: edge.relation },
      label: t(`business.relations.${edge.relation}`),
      markerEnd: { type: MarkerType.ArrowClosed },
    })),
  );
  const [selectedNodeId, setSelectedNodeId] = useState<string>();
  const [selectedEdgeId, setSelectedEdgeId] = useState<string>();

  // Derive issue overlays without rebuilding stored positions or emitting a
  // false document edit. Validation does not mutate the business assembly.
  const displayNodes = useMemo(
    () =>
      nodes.map((node) => ({
        ...node,
        data: {
          ...node.data,
          issues: issues.filter((issue) => issue.node_id === node.id),
        },
      })),
    [nodes, issues],
  );

  const selectedNode = useMemo(() => {
    const flowNode = nodes.find((node) => node.id === selectedNodeId);
    if (!flowNode) return undefined;
    const businessNode: BusinessNode = {
      id: flowNode.id,
      kind: flowNode.data.kind,
      name: flowNode.data.name,
      ref: flowNode.data.ref,
      asset: flowNode.data.asset,
      position: flowNode.position,
    };
    return businessNode;
  }, [nodes, selectedNodeId]);

  const currentAssembly = useMemo(() => toAssembly(nodes, edges), [nodes, edges]);

  // commitAssembly applies gateway-scope reconciliation at the single write
  // boundary. Camera and relationship edits therefore clear dependent assets
  // immediately, even when the affected model/companion is not selected.
  const commitAssembly = useCallback(
    (nextNodes: BusinessFlowNodeType[], nextEdges: BusinessFlowEdge[]) => {
      const next = clearChangedGatewayAssets(
        currentAssembly,
        toAssembly(nextNodes, nextEdges),
      );
      const assets = new Map(next.nodes.map((node) => [node.id, node.asset]));
      const reconciledNodes = nextNodes.map((flowNode) => ({
        ...flowNode,
        data: {
          ...flowNode.data,
          asset: assets.get(flowNode.id) ?? flowNode.data.asset,
        },
      }));
      setNodes(reconciledNodes);
      setEdges(nextEdges);
      onAssemblyChange(next);
    },
    [currentAssembly, onAssemblyChange],
  );

  // addNode supports both native drag/drop and keyboard activation of a
  // palette button. Empty assets are intentional: server validation identifies
  // required references without inventing deployment defaults.
  const addNode = useCallback(
    (kind: BusinessNodeKind, position: { x: number; y: number }) => {
      const node: BusinessFlowNodeType = {
        id: newUUID(),
        type: "businessNode",
        position,
        data: {
          kind,
          name: t(`business.defaults.${kind}`),
          ref: "",
          asset: {},
          issues: [],
        },
      };
      const nextNodes = [...nodes, node];
      commitAssembly(nextNodes, edges);
      setSelectedNodeId(node.id);
      setSelectedEdgeId(undefined);
    },
    [nodes, edges, commitAssembly, t],
  );

  // handleNodesChange persists position and removal changes only. Selection
  // and measurement changes remain local to avoid marking the draft dirty.
  const handleNodesChange = useCallback(
    (changes: NodeChange<BusinessFlowNodeType>[]) => {
      const removed = new Set(
        changes.filter((change) => change.type === "remove").map((change) => change.id),
      );
      const nextNodes = applyNodeChanges(changes, nodes);
      const nextEdges = removed.size
        ? edges.filter((edge) => !removed.has(edge.source) && !removed.has(edge.target))
        : edges;
      if (changes.some((change) => change.type === "position" || change.type === "remove")) {
        commitAssembly(nextNodes, nextEdges);
      } else {
        setNodes(nextNodes);
      }
      if (removed.size && selectedNodeId && removed.has(selectedNodeId)) {
        setSelectedNodeId(undefined);
      }
    },
    [nodes, edges, commitAssembly, selectedNodeId],
  );

  // handleEdgesChange persists deletion but not transient selection state.
  const handleEdgesChange = useCallback(
    (changes: EdgeChange<BusinessFlowEdge>[]) => {
      const nextEdges = applyEdgeChanges(changes, edges);
      if (changes.some((change) => change.type === "remove")) {
        commitAssembly(nodes, nextEdges);
      } else {
        setEdges(nextEdges);
      }
      if (selectedEdgeId && changes.some((change) => change.type === "remove" && change.id === selectedEdgeId)) {
        setSelectedEdgeId(undefined);
      }
    },
    [edges, nodes, commitAssembly, selectedEdgeId],
  );

  // handleConnect infers the relation from asset kinds and rejects duplicates,
  // cycles, dangling endpoints, and every non-business edge combination.
  const handleConnect = useCallback(
    (connection: Connection) => {
      const current = toAssembly(nodes, edges);
      const validation = validateBusinessConnection(current, connection);
      if (!validation.valid || !validation.relation) {
        message.error(t(`business.connectionErrors.${validation.reason ?? "unsupportedRelation"}`));
        return;
      }
      const relation = validation.relation;
      const edge: BusinessFlowEdge = {
        id: newUUID(),
        source: connection.source,
        target: connection.target,
        sourceHandle: connection.sourceHandle,
        targetHandle: connection.targetHandle,
        data: { relation },
        label: t(`business.relations.${relation}`),
        markerEnd: { type: MarkerType.ArrowClosed },
      };
      const nextEdges = [...edges, edge];
      commitAssembly(nodes, nextEdges);
    },
    [nodes, edges, commitAssembly, message, t],
  );

  // deleteSelection removes the explicitly selected node (including attached
  // edges) or edge. React Flow's Delete/Backspace handling remains available
  // for keyboard and multi-selection deletion.
  const deleteSelection = useCallback(() => {
    if (selectedNodeId) {
      const nextNodes = nodes.filter((node) => node.id !== selectedNodeId);
      const nextEdges = edges.filter(
        (edge) => edge.source !== selectedNodeId && edge.target !== selectedNodeId,
      );
      commitAssembly(nextNodes, nextEdges);
      setSelectedNodeId(undefined);
      return;
    }
    if (selectedEdgeId) {
      const nextEdges = edges.filter((edge) => edge.id !== selectedEdgeId);
      commitAssembly(nodes, nextEdges);
      setSelectedEdgeId(undefined);
    }
  }, [selectedNodeId, selectedEdgeId, nodes, edges, commitAssembly]);

  // updateSelectedNode writes the dedicated inspector's nested asset values
  // back to the same stable node ID.
  const updateSelectedNode = useCallback(
    (updated: BusinessNode) => {
      const nextNodes = nodes.map((node) =>
        node.id === updated.id
          ? {
              ...node,
              data: {
                ...node.data,
                name: updated.name,
                ref: updated.ref,
                asset: updated.asset,
              },
            }
          : node,
      );
      commitAssembly(nextNodes, edges);
    },
    [nodes, edges, commitAssembly],
  );

  return (
    <div
      style={{
        display: "flex",
        flexWrap: "wrap",
        alignItems: "stretch",
        gap: 12,
        width: "100%",
      }}
    >
      <aside
        aria-label={t("business.canvas.palette")}
        style={{
          flex: "0 1 180px",
          width: 180,
          minWidth: 150,
          maxHeight: 680,
          overflow: "auto",
          padding: 12,
          border: "1px solid #f0f0f0",
          borderRadius: 8,
          background: "#fff",
        }}
      >
        <Typography.Text strong style={{ display: "block", marginBottom: 4 }}>
          {t("business.canvas.palette")}
        </Typography.Text>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t("business.canvas.paletteHint")}
        </Typography.Paragraph>
        {BUSINESS_NODE_KINDS.map((kind) => (
          <button
            key={kind}
            type="button"
            draggable
            aria-label={t("business.canvas.addNode", {
              kind: t(`business.kinds.${kind}`),
            })}
            onClick={() =>
              addNode(kind, {
                x: 80 + (nodes.length % 5) * 36,
                y: 80 + (nodes.length % 5) * 72,
              })
            }
            onDragStart={(event) => {
              event.dataTransfer.setData(BUSINESS_DRAG_MIME, kind);
              event.dataTransfer.effectAllowed = "copy";
            }}
            style={{
              display: "block",
              width: "100%",
              marginBottom: 8,
              padding: "9px 10px",
              border: "1px solid #f0f0f0",
              borderLeft: `4px solid ${BUSINESS_NODE_COLORS[kind]}`,
              borderRadius: 6,
              background: "#fff",
              textAlign: "left",
              cursor: "grab",
            }}
          >
            {t(`business.kinds.${kind}`)}
          </button>
        ))}
      </aside>

      <section
        aria-label={t("business.canvas.assembly")}
        style={{
          flex: "1 1 560px",
          minWidth: 360,
          minHeight: 600,
          overflow: "hidden",
          border: "1px solid #f0f0f0",
          borderRadius: 8,
          background: "#fff",
        }}
      >
        <Space
          wrap
          style={{
            width: "100%",
            justifyContent: "space-between",
            padding: "8px 12px",
            borderBottom: "1px solid #f0f0f0",
          }}
        >
          <Typography.Text type="secondary">
            {t("business.canvas.relationHint")}
          </Typography.Text>
          <Button
            danger
            size="small"
            icon={<DeleteOutlined />}
            disabled={!selectedNodeId && !selectedEdgeId}
            onClick={deleteSelection}
          >
            {t("business.canvas.deleteSelected")}
          </Button>
        </Space>
        <div style={{ height: 620, minHeight: 600 }}>
          <ReactFlow
            nodes={displayNodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={handleNodesChange}
            onEdgesChange={handleEdgesChange}
            onConnect={handleConnect}
            onDrop={(event: DragEvent) => {
              event.preventDefault();
              const kind = event.dataTransfer.getData(BUSINESS_DRAG_MIME) as BusinessNodeKind;
              if (!BUSINESS_NODE_KINDS.includes(kind)) return;
              addNode(
                kind,
                reactFlow.screenToFlowPosition({ x: event.clientX, y: event.clientY }),
              );
            }}
            onDragOver={(event: DragEvent) => {
              event.preventDefault();
              event.dataTransfer.dropEffect = "copy";
            }}
            onNodeClick={(_, node) => {
              setSelectedNodeId(node.id);
              setSelectedEdgeId(undefined);
            }}
            onEdgeClick={(_, edge) => {
              setSelectedEdgeId(edge.id);
              setSelectedNodeId(undefined);
            }}
            onPaneClick={() => {
              setSelectedNodeId(undefined);
              setSelectedEdgeId(undefined);
            }}
            fitView
            minZoom={0.2}
            deleteKeyCode={["Backspace", "Delete"]}
          >
            <Background />
            <Controls />
            <MiniMap pannable zoomable />
          </ReactFlow>
        </div>
      </section>

      <BusinessInspector
        node={selectedNode}
        assembly={currentAssembly}
        parameterSchema={parameterSchema}
        parameters={parameters}
        showParameters={showParameters}
        onNodeChange={updateSelectedNode}
        onParametersChange={onParametersChange}
      />
    </div>
  );
}
