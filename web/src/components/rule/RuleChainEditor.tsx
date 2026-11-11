// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RuleChainEditor is the React Flow canvas plus toolbar: node palette,
// config drawer, save draft, activate and non-destructive test delivery
// with debug highlight. Every save creates a new immutable version.

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  addEdge,
  useEdgesState,
  useNodesState,
  useReactFlow,
  type Connection,
  type Edge,
  type NodeTypes,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  App as AntApp,
  Button,
  Card,
  Input,
  Popconfirm,
  Space,
  Typography,
} from "antd";
import {
  BugOutlined,
  CheckCircleOutlined,
  PlayCircleOutlined,
  QuestionCircleOutlined,
  SaveOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router-dom";

import RuleNode, { type RuleNodeType } from "./RuleNode";
import RuleNodePalette from "./RuleNodePalette";
import NodeConfigDrawer from "./NodeConfigDrawer";
import RuleTraceDrawer from "./RuleTraceDrawer";
import { catalogByType, nodeLabelKey, type NodeConfig } from "./nodeCatalog";
import {
  activateChain,
  createDraft,
  dryRun,
  getGraph,
  listChainVersions,
  type GraphDTO,
} from "../../api/ruleChains";
import { extractError } from "../../api/client";
import { newUUID } from "../../utils/id";
import { useHelpStore } from "../../help/helpStore";

// nodeTypes registers the custom node renderer.
const nodeTypes: NodeTypes = { ruleNode: RuleNode };

// RuleChainEditor renders the whole editor screen.
export default function RuleChainEditor() {
  return (
    <ReactFlowProvider>
      <EditorInner />
    </ReactFlowProvider>
  );
}

// EditorInner contains canvas state and actions.
function EditorInner() {
  const { groupId = "" } = useParams();
  const { t } = useTranslation();
  const { message } = AntApp.useApp();

  const [nodes, setNodes, onNodesChange] = useNodesState<RuleNodeType>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [selectedId, setSelectedId] = useState<string | undefined>();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [sample, setSample] = useState(
    '{"temperature": 85, "humidity": 40}',
  );
  const [visited, setVisited] = useState<Set<string>>(new Set());
  const [errored, setErrored] = useState<Set<string>>(new Set());
  const [loadedVersion, setLoadedVersion] = useState(0);
  const [activeChainId, setActiveChainId] = useState("");
  const [traceOpen, setTraceOpen] = useState(false);
  const reactFlow = useReactFlow();

  // Load the newest active version of the group, falling back to its latest
  // version when the group has never been activated.
  useEffect(() => {
    let cancelled = false;
    listChainVersions(groupId)
      .then(async (versions) => {
        const target =
          versions.find((version) => version.state === "ACTIVE") ?? versions[0];
        if (!target) {
          return;
        }
        const graph = await getGraph(target.id);
        if (cancelled) {
          return;
        }
        setLoadedVersion(target.version);
        setActiveChainId(target.state === "ACTIVE" ? target.id : "");
        setNodes(fromGraphNodes(graph));
        setEdges(fromGraphEdges(graph));
      })
      .catch((error) => {
        if (!cancelled) {
          message.error(extractError(error).message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [groupId, setNodes, setEdges, message]);

  // Apply debug flags whenever the visited sets change.
  useEffect(() => {
    setNodes((current) =>
      current.map((node) => ({
        ...node,
        data: {
          ...node.data,
          debug: visited.has(node.id),
          debugError: errored.has(node.id),
        },
      })),
    );
  }, [visited, errored, setNodes]);

  // applyTrace highlights the visited/error nodes of a selected live trace.
  const applyTrace = useCallback(
    (visitedIds: string[], errorIds: string[]) => {
      setVisited(new Set(visitedIds));
      setErrored(new Set(errorIds));
    },
    [],
  );

  // focusNode centers a node when a trace step is clicked.
  const focusNode = useCallback(
    (nodeId: string) => {
      const node = reactFlow.getNode(nodeId);
      if (!node) return;
      const width = node.measured?.width ?? 160;
      const height = node.measured?.height ?? 40;
      void reactFlow.setCenter(
        node.position.x + width / 2,
        node.position.y + height / 2,
        { zoom: 1.2, duration: 400 },
      );
    },
    [reactFlow],
  );

  // onConnect creates a labeled edge; the source handle id is the output
  // relation.
  const onConnect = useCallback(
    (connection: Connection) => {
      const relation = connection.sourceHandle ?? "Success";
      setEdges((current) =>
        addEdge(
          { ...connection, label: relation, animated: visited.has(connection.source) },
          current,
        ),
      );
    },
    [setEdges, visited],
  );

  // onDrop creates a node from the palette at the drop position.
  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();
      const nodeType = event.dataTransfer.getData("application/x-rule-node");
      const catalog = catalogByType[nodeType];
      if (!catalog) return;

      const id = newUUID();
      const position = { x: event.clientX - 260, y: event.clientY - 120 };
      const node: RuleNodeType = {
        id,
        type: "ruleNode",
        position,
        data: {
          label: t(nodeLabelKey[nodeType] ?? nodeType),
          nodeType,
          config: { ...catalog.defaultConfig },
        },
      };
      setNodes((current) => [...current, node]);
      setSelectedId(id);
      setDrawerOpen(true);
    },
    [setNodes, t],
  );

  // selectedNode resolves the drawer target.
  const selectedNode = useMemo(
    () => nodes.find((node) => node.id === selectedId),
    [nodes, selectedId],
  );

  // updateNode writes drawer edits back to node data.
  const updateNode = useCallback(
    (id: string, label: string, config: NodeConfig) => {
      setNodes((current) =>
        current.map((node) =>
          node.id === id ? { ...node, data: { ...node.data, label, config } } : node,
        ),
      );
    },
    [setNodes],
  );

  // deleteNode removes the node and its edges.
  const deleteNode = useCallback(
    (id: string) => {
      setNodes((current) => current.filter((node) => node.id !== id));
      setEdges((current) =>
        current.filter((edge) => edge.source !== id && edge.target !== id),
      );
      setDrawerOpen(false);
    },
    [setNodes, setEdges],
  );

  // toGraph converts the canvas into the persistable DTO.
  const toGraph = useCallback((): GraphDTO => ({
    nodes: nodes.map((node) => ({
      id: node.id,
      type: node.data.nodeType,
      name: node.data.label,
      config: node.data.config as Record<string, unknown>,
      position_x: node.position.x,
      position_y: node.position.y,
    })),
    edges: edges.map((edge) => ({
      from: edge.source,
      to: edge.target,
      relation: (edge.label as string) ?? edge.sourceHandle ?? "Success",
    })),
  }), [nodes, edges]);

  // applyVersion loads one stored version onto the canvas.
  const applyVersion = useCallback(
    async (chainID: string, version: number) => {
      const graph = await getGraph(chainID);
      setLoadedVersion(version);
      setNodes(fromGraphNodes(graph));
      setEdges(fromGraphEdges(graph));
    },
    [setNodes, setEdges],
  );

  // saveDraft persists the canvas as a new group version and loads it.
  const saveDraft = useCallback(async (): Promise<string | null> => {
    try {
      const result = await createDraft(groupId, toGraph());
      const nextVersion = loadedVersion + 1;
      message.success(t("rule.toasts.savedVersion", { count: result.graph.nodes.length }));
      await applyVersion(result.chain_id, nextVersion);
      return result.chain_id;
    } catch (error) {
      message.error(extractError(error).message);
      return null;
    }
  }, [groupId, toGraph, applyVersion, loadedVersion, message, t]);

  // saveAndActivate saves a new version and activates it.
  const saveAndActivate = useCallback(async () => {
    const newId = await saveDraft();
    if (!newId) return;
    try {
      await activateChain(newId);
      message.success(t("rule.toasts.activated"));
    } catch (error) {
      message.error(extractError(error).message);
    }
  }, [saveDraft, message, t]);

  // runTest executes the dry-run with the sample payload and highlights
  // the visited path.
  const runTest = useCallback(async () => {
    let payload: Record<string, unknown>;
    try {
      payload = JSON.parse(sample);
    } catch {
      message.error(t("rule.toasts.invalidSample"));
      return;
    }
    try {
      const trace = await dryRun(toGraph(), payload, {});
      const visitedSet = new Set<string>();
      const errorSet = new Set<string>();
      for (const record of trace.records) {
        visitedSet.add(record.node_id);
        if (record.error) errorSet.add(record.node_id);
      }
      setVisited(visitedSet);
      setErrored(errorSet);
      setEdges((current) =>
        current.map((edge) => ({
          ...edge,
          animated: visitedSet.has(edge.source),
        })),
      );
      message.success(
        t("rule.toasts.testResult", {
          nodes: trace.records.length,
          errors: errorSet.size,
        }),
      );
    } catch (error) {
      message.error(extractError(error).message);
    }
  }, [toGraph, sample, message, setEdges, t]);

  return (
    <>
      <Card
        size="small"
        style={{ marginBottom: 12, borderRadius: 8 }}
        styles={{ body: { padding: "8px 12px" } }}
      >
        <Space wrap style={{ justifyContent: "space-between", width: "100%" }}>
          <Space wrap>
            <Button
              type="primary"
              icon={<SaveOutlined />}
              onClick={() => void saveDraft()}
            >
              {t("rule.controls.saveNewVersion")}
            </Button>
            <Popconfirm
              title={t("rule.confirm.activateTitle")}
              description={t("rule.confirm.activateDesc")}
              onConfirm={() => void saveAndActivate()}
            >
              <Button icon={<CheckCircleOutlined />}>{t("rule.controls.saveActivate")}</Button>
            </Popconfirm>
            <Button icon={<BugOutlined />} onClick={() => setTraceOpen(true)}>
              {t("rule.controls.trace")}
            </Button>
          </Space>
          <Space>
            <Button
              type="text"
              icon={<QuestionCircleOutlined />}
              onClick={() => useHelpStore.getState().open("rule-chain-editor")}
            />
            <Typography.Text type="secondary">{t("rule.testPayload")}</Typography.Text>
            <Input
              value={sample}
              onChange={(event) => setSample(event.target.value)}
              style={{ width: 320 }}
            />
            <Button icon={<PlayCircleOutlined />} onClick={() => void runTest()}>
              {t("rule.controls.testDelivery")}
            </Button>
          </Space>
        </Space>
      </Card>

      <div style={{ display: "flex", height: "calc(100vh - 170px)" }}>
        <RuleNodePalette />
        <div style={{ flex: 1, position: "relative" }}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onDrop={onDrop}
            onDragOver={(event) => {
              event.preventDefault();
              event.dataTransfer.dropEffect = "move";
            }}
            onNodeClick={(_, node) => {
              setSelectedId(node.id);
              setDrawerOpen(true);
            }}
            onPaneClick={() => setDrawerOpen(false)}
            fitView
            deleteKeyCode={["Backspace", "Delete"]}
          >
            <Background />
            <Controls />
            <MiniMap pannable zoomable />
          </ReactFlow>
        </div>
      </div>

      <NodeConfigDrawer
        open={drawerOpen}
        node={selectedNode}
        onChange={updateNode}
        onDelete={deleteNode}
        onClose={() => setDrawerOpen(false)}
      />

      <RuleTraceDrawer
        open={traceOpen}
        onClose={() => setTraceOpen(false)}
        chainId={activeChainId}
        nodes={nodes}
        onApplyTrace={applyTrace}
        onFocusNode={focusNode}
      />
    </>
  );
}

// fromGraphNodes converts stored nodes into React Flow nodes, applying a
// simple staggered fallback layout when positions are absent.
function fromGraphNodes(graph: GraphDTO): RuleNodeType[] {
  return graph.nodes.map((node, index) => ({
    id: node.id,
    type: "ruleNode",
    position: {
      x: node.position_x ?? 80 + index * 40,
      y: node.position_y ?? 80 + index * 110,
    },
    data: {
      label: node.name,
      nodeType: node.type,
      config: node.config as NodeConfig,
    },
  }));
}

// fromGraphEdges converts stored edges, labeling them with the relation.
function fromGraphEdges(graph: GraphDTO): Edge[] {
  return graph.edges.map((edge) => ({
    id: `${edge.from}-${edge.relation}-${edge.to}`,
    source: edge.from,
    target: edge.to,
    sourceHandle: edge.relation,
    label: edge.relation,
  }));
}
