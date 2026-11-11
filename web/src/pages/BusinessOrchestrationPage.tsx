// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BusinessOrchestrationPage coordinates immutable template selection, an
// assembly draft, generation-fenced saves, validation/expansion, lifecycle
// actions, and on-demand monitoring. Queries run on mount or explicit actions;
// this page deliberately adds no polling and no unsupported WebSocket type.

import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Space,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  CheckCircleOutlined,
  CodeOutlined,
  RocketOutlined,
  RollbackOutlined,
  SaveOutlined,
  StopOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import { extractError } from "../api/client";
import {
  createBusinessInstance,
  deleteBusinessInstance,
  deployBusinessInstance,
  getBusinessExpansion,
  listBusinessInstances,
  listBusinessTemplates,
  rollbackBusinessInstance,
  undeployBusinessInstance,
  updateBusinessInstance,
  validateBusinessInstance,
  type BusinessInstance,
  type BusinessInstanceCreate,
  type Expansion,
  type Template,
  type ValidationReport,
} from "../api/business";
import BusinessCanvas from "../components/business/BusinessCanvas";
import ExpansionDrawer from "../components/business/ExpansionDrawer";
import InstanceTable from "../components/business/InstanceTable";
import MonitorDrawer from "../components/business/MonitorDrawer";
import TemplateGallery from "../components/business/TemplateGallery";
import ValidationIssues from "../components/business/ValidationIssues";
import {
  prepareAssemblyForSave,
  templateToDraft,
} from "../components/business/businessModel";

// BusyAction identifies the one lifecycle operation currently controlling the
// editor. Saving is part of validate/expand/deploy so those actions never run
// against an older saved assembly by accident.
type BusyAction = "save" | "validate" | "expand" | "deploy" | "undeploy" | "rollback";

// MonitorSelection keeps drawer identity independent of mutable table rows.
interface MonitorSelection {
  id: string;
  name: string;
}

// BusinessOrchestrationPage renders the Templates, Canvas, and Instances tabs.
export default function BusinessOrchestrationPage() {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState("templates");
  const [draft, setDraft] = useState<BusinessInstanceCreate | null>(null);
  const [current, setCurrent] = useState<BusinessInstance>();
  const [dirty, setDirty] = useState(false);
  const [parameterSchema, setParameterSchema] = useState<Record<string, unknown>>({});
  const [validation, setValidation] = useState<ValidationReport>();
  const [editorRevision, setEditorRevision] = useState(0);
  const [busyAction, setBusyAction] = useState<BusyAction>();
  const [deletingId, setDeletingId] = useState<string>();
  const [expansion, setExpansion] = useState<Expansion>();
  const [expansionOpen, setExpansionOpen] = useState(false);
  const [monitor, setMonitor] = useState<MonitorSelection>();
  const [monitorOpen, setMonitorOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);
  const [rollbackForm] = Form.useForm<{ generation: number }>();

  const templatesQuery = useQuery({
    queryKey: ["business-templates"],
    queryFn: listBusinessTemplates,
    refetchOnWindowFocus: false,
  });
  const instancesQuery = useQuery({
    queryKey: ["business-instances"],
    queryFn: listBusinessInstances,
    refetchOnWindowFocus: false,
  });

  // applySaved synchronizes server generation and normalized documents without
  // remounting the canvas. A rollback explicitly increments editorRevision
  // because its returned assembly is intentionally different.
  const applySaved = useCallback((instance: BusinessInstance) => {
    setCurrent(instance);
    setDraft({
      name: instance.name,
      template_id: instance.template_id,
      template_version: instance.template_version,
      parameters: instance.parameters,
      assembly: instance.assembly,
    });
    setValidation(instance.validation);
    setDirty(false);
  }, []);

  // invalidateInstances refreshes operational views only after a successful
  // mutation. The monitor key is included when a lifecycle action may have
  // changed health, never on a timer.
  const invalidateInstances = useCallback(
    async (instanceId?: string) => {
      const invalidations: Promise<unknown>[] = [
        queryClient.invalidateQueries({ queryKey: ["business-instances"] }),
      ];
      if (instanceId) {
        invalidations.push(
          queryClient.invalidateQueries({ queryKey: ["business-monitor", instanceId] }),
        );
      }
      await Promise.all(invalidations);
    },
    [queryClient],
  );

  // persist saves or creates the current editor document. Raw pipeline JSON is
  // checked locally first, while all schema, graph, and asset semantics remain
  // server-authoritative.
  const persist = useCallback(async (): Promise<BusinessInstance | null> => {
    if (!draft) return null;
    if (current && !dirty) return current;
    if (!draft.name.trim()) {
      message.error(t("business.errors.instanceNameRequired"));
      return null;
    }
    const prepared = prepareAssemblyForSave(draft.assembly);
    if (prepared.invalidNodeId) {
      const node = draft.assembly.nodes.find((item) => item.id === prepared.invalidNodeId);
      message.error(
        t("business.errors.invalidAssetJson", {
          name: node?.name ?? prepared.invalidNodeId,
        }),
      );
      return null;
    }

    const saved = current
      ? await updateBusinessInstance(current.id, {
          name: draft.name.trim(),
          parameters: draft.parameters,
          assembly: prepared.assembly,
          expected_generation: current.generation,
        })
      : await createBusinessInstance({
          ...draft,
          name: draft.name.trim(),
          assembly: prepared.assembly,
        });
    applySaved(saved);
    await invalidateInstances(saved.id);
    return saved;
  }, [draft, current, dirty, applySaved, invalidateInstances, message, t]);

  // runAction serializes page lifecycle buttons and normalizes API errors.
  const runAction = useCallback(
    async (action: BusyAction, task: () => Promise<void>) => {
      setBusyAction(action);
      try {
        await task();
      } catch (error) {
        message.error(extractError(error).message);
      } finally {
        setBusyAction(undefined);
      }
    },
    [message],
  );

  // instantiate creates an independent local draft and enters the canvas.
  const instantiate = useCallback(
    (template: Template, name: string) => {
      setCurrent(undefined);
      setDraft(templateToDraft(template, name));
      setDirty(true);
      setParameterSchema(template.parameters);
      setValidation(undefined);
      setExpansion(undefined);
      setEditorRevision((revision) => revision + 1);
      setActiveTab("canvas");
    },
    [],
  );

  // openInstance loads a saved document and its matching flat parameter
  // schema. The saved generation becomes the expected fence for the next PUT.
  const openInstance = useCallback(
    (instance: BusinessInstance) => {
      const template = templatesQuery.data?.find(
        (candidate) =>
          candidate.id === instance.template_id &&
          candidate.version === instance.template_version,
      );
      setParameterSchema(template?.parameters ?? {});
      applySaved(instance);
      setExpansion(undefined);
      setEditorRevision((revision) => revision + 1);
      setActiveTab("canvas");
    },
    [applySaved, templatesQuery.data],
  );

  // save persists only; validation and deployment have their own explicit
  // actions and are never triggered as a side effect of Save.
  const save = useCallback(() => {
    void runAction("save", async () => {
      const saved = await persist();
      if (saved) message.success(t("business.toasts.saved"));
    });
  }, [runAction, persist, message, t]);

  // validate saves first, then displays the server report and node markers.
  const validate = useCallback(() => {
    void runAction("validate", async () => {
      const saved = await persist();
      if (!saved) return;
      const report = await validateBusinessInstance(saved.id);
      setValidation(report);
      setCurrent((instance) => (instance ? { ...instance, validation: report } : instance));
      await invalidateInstances(saved.id);
      if (report.valid) {
        message.success(t("business.toasts.validationPassed"));
      } else {
        message.warning(t("business.toasts.validationFailed"));
      }
    });
  }, [runAction, persist, invalidateInstances, message, t]);

  // expand previews artifacts for the exact newly saved desired generation.
  const expand = useCallback(() => {
    void runAction("expand", async () => {
      const saved = await persist();
      if (!saved) return;
      const result = await getBusinessExpansion(saved.id, saved.generation);
      setExpansion(result);
      setExpansionOpen(true);
    });
  }, [runAction, persist]);

  // deploy saves current intent before invoking server preflight and apply.
  const deploy = useCallback(() => {
    void runAction("deploy", async () => {
      const saved = await persist();
      if (!saved) return;
      const deployed = await deployBusinessInstance(saved.id);
      applySaved(deployed);
      await invalidateInstances(deployed.id);
      message.success(t("business.toasts.deployed"));
    });
  }, [runAction, persist, applySaved, invalidateInstances, message, t]);

  // undeploy acts on the saved generation and does not silently save unrelated
  // canvas edits as a new desired generation.
  const undeploy = useCallback(() => {
    if (!current) return;
    void runAction("undeploy", async () => {
      const undeployed = await undeployBusinessInstance(current.id);
      applySaved(undeployed);
      await invalidateInstances(undeployed.id);
      message.success(t("business.toasts.undeployed"));
    });
  }, [current, runAction, applySaved, invalidateInstances, message, t]);

  // remove deletes through the API and clears the editor only if it was
  // editing that same instance.
  const remove = useCallback(
    async (instance: BusinessInstance) => {
      setDeletingId(instance.id);
      try {
        await deleteBusinessInstance(instance.id);
        await invalidateInstances(instance.id);
        if (current?.id === instance.id) {
          setCurrent(undefined);
          setDraft(null);
          setDirty(false);
          setValidation(undefined);
          setEditorRevision((revision) => revision + 1);
        }
        if (monitor?.id === instance.id) setMonitorOpen(false);
        message.success(t("business.toasts.deleted"));
      } catch (error) {
        message.error(extractError(error).message);
      } finally {
        setDeletingId(undefined);
      }
    },
    [current, monitor, invalidateInstances, message, t],
  );

  // rollback re-applies the selected retained generation and remounts the
  // canvas with the returned assembly, making the generation change visible.
  const rollback = useCallback(() => {
    if (!current) return;
    void rollbackForm.validateFields().then(({ generation }) => {
      void runAction("rollback", async () => {
        const rolledBack = await rollbackBusinessInstance(current.id, generation);
        applySaved(rolledBack);
        setEditorRevision((revision) => revision + 1);
        await invalidateInstances(rolledBack.id);
        setRollbackOpen(false);
        message.success(t("business.toasts.rolledBack", { generation }));
      });
    });
  }, [current, rollbackForm, runAction, applySaved, invalidateInstances, message, t]);

  const busy = busyAction !== undefined;
  const canvasTab = (
    <>
      {!draft ? (
        <Card>
          <Empty description={t("business.canvas.empty")}>
            <Button type="primary" onClick={() => setActiveTab("templates")}>
              {t("business.canvas.chooseTemplate")}
            </Button>
          </Empty>
        </Card>
      ) : (
        <>
          <Card size="small" style={{ marginBottom: 12 }}>
            <Space direction="vertical" size={12} style={{ width: "100%" }}>
              <Space wrap style={{ width: "100%", justifyContent: "space-between" }}>
                <Space wrap>
                  <Input
                    value={draft.name}
                    maxLength={255}
                    style={{ width: 280 }}
                    aria-label={t("business.canvas.instanceName")}
                    placeholder={t("business.canvas.instanceName")}
                    onChange={(event) => {
                      setDirty(true);
                      setDraft((value) =>
                        value ? { ...value, name: event.target.value } : value,
                      );
                    }}
                  />
                  {current ? (
                    <Tag color={current.status === "RUNNING" ? "success" : "processing"}>
                      {t(`business.statuses.${current.status}`, {
                        defaultValue: current.status,
                      })}
                    </Tag>
                  ) : (
                    <Tag>{t("business.canvas.unsaved")}</Tag>
                  )}
                  {current && (
                    <Typography.Text type="secondary">
                      {t("business.canvas.generations", {
                        generation: current.generation,
                        activeGeneration: current.active_generation,
                      })}
                    </Typography.Text>
                  )}
                </Space>
                <Space wrap>
                  <Button
                    type="primary"
                    icon={<SaveOutlined />}
                    loading={busyAction === "save"}
                    disabled={busy}
                    onClick={save}
                  >
                    {current ? t("business.actions.update") : t("business.actions.save")}
                  </Button>
                  <Button
                    icon={<CheckCircleOutlined />}
                    loading={busyAction === "validate"}
                    disabled={busy}
                    onClick={validate}
                  >
                    {t("business.actions.validate")}
                  </Button>
                  <Button
                    icon={<CodeOutlined />}
                    loading={busyAction === "expand"}
                    disabled={busy}
                    onClick={expand}
                  >
                    {t("business.actions.expand")}
                  </Button>
                  <Button
                    type="primary"
                    icon={<RocketOutlined />}
                    loading={busyAction === "deploy"}
                    disabled={busy}
                    onClick={deploy}
                  >
                    {t("business.actions.deploy")}
                  </Button>
                  <Popconfirm
                    title={t("business.confirm.undeployTitle")}
                    description={t("business.confirm.undeployDescription")}
                    okText={t("business.actions.undeploy")}
                    cancelText={t("business.actions.cancel")}
                    onConfirm={undeploy}
                    disabled={!current || busy}
                  >
                    <Button
                      danger
                      icon={<StopOutlined />}
                      loading={busyAction === "undeploy"}
                      disabled={!current || busy}
                    >
                      {t("business.actions.undeploy")}
                    </Button>
                  </Popconfirm>
                  <Button
                    icon={<RollbackOutlined />}
                    disabled={!current || current.active_generation <= 0 || busy}
                    onClick={() => {
                      if (!current) return;
                      rollbackForm.setFieldsValue({
                        generation: current.active_generation,
                      });
                      setRollbackOpen(true);
                    }}
                  >
                    {t("business.actions.rollback")}
                  </Button>
                </Space>
              </Space>
              {current?.last_error && (
                <Alert
                  type="error"
                  showIcon
                  message={t("business.canvas.lastError")}
                  description={current.last_error}
                />
              )}
            </Space>
          </Card>

          {validation && (
            <Card size="small" style={{ marginBottom: 12 }}>
              <Alert
                type={validation.valid ? "success" : "error"}
                showIcon
                message={
                  validation.valid
                    ? t("business.validation.passed")
                    : t("business.validation.failed")
                }
                style={{ marginBottom: validation.issues.length ? 12 : 0 }}
              />
              <ValidationIssues issues={validation.issues} />
            </Card>
          )}

          <BusinessCanvas
            key={editorRevision}
            assembly={draft.assembly}
            issues={validation?.issues ?? []}
            parameterSchema={parameterSchema}
            parameters={draft.parameters}
            onAssemblyChange={(assembly) => {
              setDirty(true);
              setDraft((value) => (value ? { ...value, assembly } : value));
            }}
            onParametersChange={(parameters) => {
              setDirty(true);
              setDraft((value) => (value ? { ...value, parameters } : value));
            }}
          />
        </>
      )}
    </>
  );

  return (
    <>
      <PageHeader
        title={t("business.title")}
        subtitle={t("business.subtitle")}
        helpId="business-orchestration"
      />
      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: "templates",
            label: t("business.tabs.templates"),
            children: (
              <Card>
                <TemplateGallery
                  templates={templatesQuery.data ?? []}
                  loading={templatesQuery.isFetching}
                  error={templatesQuery.error ?? undefined}
                  onRefresh={() => void templatesQuery.refetch()}
                  onInstantiate={instantiate}
                  onCreated={() => {
                    void queryClient.invalidateQueries({ queryKey: ["business-templates"] });
                  }}
                  onDeleted={() => {
                    void queryClient.invalidateQueries({ queryKey: ["business-templates"] });
                  }}
                />
              </Card>
            ),
          },
          { key: "canvas", label: t("business.tabs.canvas"), children: canvasTab },
          {
            key: "instances",
            label: t("business.tabs.instances"),
            children: (
              <Card>
                <InstanceTable
                  instances={instancesQuery.data ?? []}
                  loading={instancesQuery.isFetching}
                  error={instancesQuery.error ?? undefined}
                  deletingId={deletingId}
                  onRefresh={() => void instancesQuery.refetch()}
                  onEdit={openInstance}
                  onMonitor={(instance) => {
                    setMonitor({ id: instance.id, name: instance.name });
                    setMonitorOpen(true);
                  }}
                  onDelete={(instance) => void remove(instance)}
                />
              </Card>
            ),
          },
        ]}
      />

      <ExpansionDrawer
        expansion={expansion}
        open={expansionOpen}
        onClose={() => setExpansionOpen(false)}
      />
      <MonitorDrawer
        instanceId={monitor?.id}
        instanceName={monitor?.name}
        open={monitorOpen}
        onClose={() => setMonitorOpen(false)}
      />
      <Drawer
        open={rollbackOpen}
        width={480}
        title={t("business.rollback.title")}
        onClose={() => setRollbackOpen(false)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setRollbackOpen(false)}>
              {t("business.actions.cancel")}
            </Button>
            <Button
              type="primary"
              loading={busyAction === "rollback"}
              onClick={rollback}
            >
              {t("business.actions.rollback")}
            </Button>
          </Space>
        }
      >
        <Typography.Paragraph type="secondary">
          {t("business.rollback.description")}
        </Typography.Paragraph>
        <Form form={rollbackForm} layout="vertical">
          <Form.Item
            name="generation"
            label={t("business.rollback.generation")}
            rules={[
              {
                required: true,
                message: t("business.rollback.generationRequired"),
              },
            ]}
          >
            <InputNumber min={1} precision={0} style={{ width: "100%" }} />
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
}
