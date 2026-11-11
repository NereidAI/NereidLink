// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OperationsTab lists the operations a gateway's firmware supports and
// lets an operator execute them with schema-driven parameters (ADR-0035).
// Risk tiers are enforced per catalog descriptor: read executes directly,
// normal requires a confirmation, dangerous requires a fresh TOTP step-up
// with a signed QR payload for verification. The newest task history is
// shown below; nothing is ever queued for an offline gateway.

import {
  Alert,
  Button,
  Card,
  Descriptions,
  Divider,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { EyeOutlined } from "@ant-design/icons";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery } from "@tanstack/react-query";

import type { Device } from "../../api/devices";
import {
  listGatewayOperations,
  type OperationView,
} from "../../api/gatewayOperations";
import {
  invokeOperation,
  listOperationTasks,
  type OperationTask,
} from "../../api/operationTasks";
import StepUpCard from "../StepUpCard";
import SchemaFields from "../schema/SchemaFields";
import { hasRenderableSchema } from "../schema/schemaFieldsUtils";
import FilterSearchBar from "../FilterSearchBar";
import { extractError } from "../../api/client";

interface OperationsTabProps {
  // device is the gateway whose operations are managed.
  device: Device;
}

// taskStatusColor maps a terminal status to its tag color.
function taskStatusColor(status: string): string {
  switch (status) {
    case "SUCCEEDED":
      return "success";
    case "FAILED":
      return "error";
    case "TIMEOUT":
      return "warning";
    default:
      return "default";
  }
}

// localizedName renders the display name in the active UI language,
// falling back to English and finally the stable operation key.
function localizedName(view: OperationView, lang: string): string {
  const key = lang.startsWith("zh") ? "zh" : "en";
  return view.display_name?.[key] || view.display_name?.en || view.operation;
}

// riskColor maps a descriptor risk level to its tag color.
function riskColor(risk: string): string {
  switch (risk) {
    case "dangerous":
      return "red";
    case "normal":
      return "orange";
    default:
      return "blue";
  }
}

// OperationsTab renders the catalog, invoke modal and task history.
export default function OperationsTab({ device }: OperationsTabProps) {
  const { t, i18n } = useTranslation();
  const catalogQuery = useQuery({
    queryKey: ["gateway-operations", device.id],
    queryFn: () => listGatewayOperations(device.id),
  });
  const historyQuery = useQuery({
    queryKey: ["operation-tasks", device.id],
    queryFn: () => listOperationTasks(device.id),
  });

  // record holds the history row whose full details are shown in a drawer.
  const [record, setRecord] = useState<OperationTask | null>(null);

  // catalog filters
  const [catalogKeyword, setCatalogKeyword] = useState("");
  const [catalogRisk, setCatalogRisk] = useState("");

  // history filters
  const [historyKeyword, setHistoryKeyword] = useState("");
  const [historyStatus, setHistoryStatus] = useState("");

  // The gateway endpoint already scopes the catalog to this gateway's own
  // advertisements plus tenant-private fallbacks; disabled entries are
  // hidden from execution.
  const available = useMemo(
    () => (catalogQuery.data ?? []).filter((view) => !view.disabled),
    [catalogQuery.data],
  );

  // filteredCatalog applies keyword and risk filters to the catalog.
  const filteredCatalog = useMemo(() => {
    const needle = catalogKeyword.trim().toLowerCase();
    const lang = i18n.language;
    return available.filter((view) => {
      if (catalogRisk && view.risk !== catalogRisk) return false;
      if (!needle) return true;
      const haystack = [
        view.operation,
        localizedName(view, lang),
        view.description?.en ?? "",
        view.description?.zh ?? "",
      ]
        .join("\n")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [available, catalogKeyword, catalogRisk, i18n.language]);

  // filteredHistory applies keyword and status filters to task history.
  const filteredHistory = useMemo(() => {
    const needle = historyKeyword.trim().toLowerCase();
    return (historyQuery.data ?? []).filter((task) => {
      if (historyStatus && task.status !== historyStatus) return false;
      if (!needle) return true;
      const haystack = [
        task.operation,
        task.invoked_by_email ?? "",
        task.reason ?? "",
      ]
        .join("\n")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [historyQuery.data, historyKeyword, historyStatus]);

  const [target, setTarget] = useState<OperationView | null>(null);

  const catalogColumns = [
    {
      title: t("pages.deviceDetail.operations.columns.operation"),
      render: (_: unknown, row: OperationView) => (
        <Space size={6}>
          {localizedName(row, i18n.language)}
          {row.type_version && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              v{row.type_version}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.deviceDetail.operations.columns.risk"),
      dataIndex: "risk",
      render: (risk: string) => (
        <Tag color={riskColor(risk)}>{t(`pages.deviceDetail.operations.risks.${risk}`)}</Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.operations.columns.timeout"),
      dataIndex: "timeout_ms",
      render: (ms: number) =>
        t("pages.deviceDetail.operations.timeoutMs", { ms }),
    },
    {
      title: "",
      render: (_: unknown, row: OperationView) => (
        <Button size="small" onClick={() => setTarget(row)}>
          {t("pages.deviceDetail.operations.execute")}
        </Button>
      ),
    },
  ];

  const historyColumns = [
    {
      title: t("pages.deviceDetail.operations.columns.started"),
      dataIndex: "started_at",
    },
    {
      title: t("pages.deviceDetail.operations.columns.operation"),
      dataIndex: "operation",
    },
    {
      title: t("pages.deviceDetail.operations.columns.risk"),
      dataIndex: "risk",
      render: (risk: string) => (
        <Tag color={riskColor(risk)}>{t(`pages.deviceDetail.operations.risks.${risk}`)}</Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.operations.columns.status"),
      dataIndex: "status",
      render: (status: string) => (
        <Tag color={taskStatusColor(status)}>
          {t(`pages.deviceDetail.operations.statuses.${status}`)}
        </Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.operations.columns.operator"),
      dataIndex: "invoked_by_email",
    },
    {
      title: "",
      width: 60,
      render: (_: unknown, row: OperationTask) => (
        <Button
          size="small"
          type="text"
          icon={<EyeOutlined />}
          onClick={() => setRecord(row)}
        />
      ),
    },
  ];

  const statusFilterOptions = [
    "PENDING",
    "SUCCEEDED",
    "FAILED",
    "TIMEOUT",
    "NOT_CONNECTED",
  ].map((value) => ({
    value,
    label: t(`pages.deviceDetail.operations.statuses.${value}`),
  }));

  const riskFilterOptions = ["read", "normal", "dangerous"].map((value) => ({
    value,
    label: t(`pages.deviceDetail.operations.risks.${value}`),
  }));

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="middle">
      <Card size="small">
        <FilterSearchBar
          searchValue={catalogKeyword}
          onSearchChange={setCatalogKeyword}
          searchPlaceholder={t(
            "pages.deviceDetail.operations.catalogSearchPlaceholder",
          )}
          filters={[
            {
              key: "risk",
              placeholder: t("pages.operations.filters.riskAll"),
              value: catalogRisk,
              onChange: setCatalogRisk,
              options: riskFilterOptions,
            },
          ]}
        />
        <Table
          rowKey={(row) => `${row.operation}|${row.type_version}`}
          size="small"
          loading={catalogQuery.isLoading}
          dataSource={filteredCatalog}
          columns={catalogColumns}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <Divider style={{ margin: "4px 0" }} />
      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("pages.deviceDetail.operations.history")}
      </Typography.Title>
      <FilterSearchBar
        searchValue={historyKeyword}
        onSearchChange={setHistoryKeyword}
        searchPlaceholder={t(
          "pages.deviceDetail.operations.historySearchPlaceholder",
        )}
        filters={[
          {
            key: "status",
            placeholder: t(
              "pages.deviceDetail.operations.filters.statusAll",
            ),
            value: historyStatus,
            onChange: setHistoryStatus,
            options: statusFilterOptions,
          },
        ]}
      />
      <Table
        rowKey="id"
        size="small"
        loading={historyQuery.isLoading}
        dataSource={filteredHistory}
        columns={historyColumns}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      {target && (
        <InvokeModal
          target={target}
          gatewayId={device.id}
          onClose={() => setTarget(null)}
          onDone={() => historyQuery.refetch()}
        />
      )}
      {record && (
        <OperationRecordDrawer record={record} onClose={() => setRecord(null)} />
      )}
    </Space>
  );
}

interface InvokeModalProps {
  // target is the selected descriptor.
  target: OperationView;
  // gatewayId is the target gateway.
  gatewayId: string;
  // onClose dismisses the modal.
  onClose: () => void;
  // onDone refetches history after a terminal invocation.
  onDone: () => void;
}

// InvokeModal handles params, dangerous-action step-up and result display.
function InvokeModal({ target, gatewayId, onClose, onDone }: InvokeModalProps) {
  const { t, i18n } = useTranslation();
  const [form] = Form.useForm();
  const dangerous = target.risk === "dangerous";
  const normal = target.risk === "normal";

  const renderable =
    target.param_schema != null && hasRenderableSchema(target.param_schema);

  // stepUpId holds the verified challenge id required for dangerous ops.
  const [stepUpId, setStepUpId] = useState("");
  const [task, setTask] = useState<OperationTask | null>(null);
  const [error, setError] = useState<string>("");
  // timeoutMs is kept outside the params form; empty means the descriptor
  // default. It can only shorten that default.
  const [timeoutMs, setTimeoutMs] = useState<number | null>(null);

  const invokeMutation = useMutation({
    mutationFn: async () => {
      const values = await form.validateFields();
      let params: Record<string, unknown> = {};
      if (renderable) {
        params = values as Record<string, unknown>;
      } else if (values.raw) {
        params = JSON.parse(values.raw) as Record<string, unknown>;
      }
      return invokeOperation(
        gatewayId,
        target.operation,
        params,
        dangerous ? stepUpId : undefined,
        timeoutMs ?? undefined,
      );
    },
    onSuccess: (result) => {
      setTask(result);
      onDone();
    },
    onError: (err) => setError(extractError(err).message),
  });

  const executeButton = (
    <Button
      type="primary"
      danger={normal}
      loading={invokeMutation.isPending}
      disabled={dangerous && stepUpId === ""}
      onClick={() => {
        if (!normal) invokeMutation.mutate();
      }}
    >
      {normal || dangerous
        ? t("pages.deviceDetail.operations.confirmExecute")
        : t("pages.deviceDetail.operations.execute")}
    </Button>
  );

  return (
    <Modal
      open
      width={520}
      title={localizedName(target, i18n.language)}
      onCancel={onClose}
      footer={
        task ? (
          <Button onClick={onClose}>{t("common.close")}</Button>
        ) : (
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            {normal ? (
              <Popconfirm
                okText={t(
                  "pages.deviceDetail.operations.confirmExecute",
                )}
                cancelText={t("common.cancel")}
                okButtonProps={{ danger: true }}
                title={t("pages.deviceDetail.operations.confirmTitle", {
                  name: localizedName(target, i18n.language),
                })}
                description={t("pages.deviceDetail.operations.confirmHint")}
                onConfirm={() => invokeMutation.mutate()}
              >
                {executeButton}
              </Popconfirm>
            ) : (
              executeButton
            )}
          </Space>
        )
      }
    >
      {error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={error}
          closable
          onClose={() => setError("")}
        />
      )}

      {task ? (
        <Space direction="vertical" style={{ width: "100%" }}>
          <Tag color={taskStatusColor(task.status)}>
            {t(`pages.deviceDetail.operations.statuses.${task.status}`)}
          </Tag>
          {task.reason && (
            <Alert type="warning" showIcon message={task.reason} />
          )}
          {task.result != null && (
            <Input.TextArea
              readOnly
              autoSize={{ minRows: 2, maxRows: 14 }}
              value={JSON.stringify(task.result, null, 2)}
            />
          )}
        </Space>
      ) : (
        <Form form={form} layout="vertical">
          {renderable ? (
            // Fields are rendered at the form root: on submit the validated
            // values are themselves the params object.
            <SchemaFields schema={target.param_schema} />
          ) : (
            <Form.Item
              name="raw"
              label={t("pages.deviceDetail.operations.rawParams")}
              initialValue="{}"
            >
              <Input.TextArea autoSize={{ minRows: 3, maxRows: 10 }} />
            </Form.Item>
          )}

          <Form.Item
            label={t("pages.deviceDetail.operations.timeoutOverride")}
            tooltip={t("pages.deviceDetail.operations.timeoutOverrideHint", {
              ms: target.timeout_ms,
            })}
            style={{ marginBottom: 8 }}
          >
            <InputNumber
              min={100}
              max={target.timeout_ms}
              step={100}
              value={timeoutMs}
              onChange={setTimeoutMs}
              addonAfter="ms"
              placeholder={t(
                "pages.deviceDetail.operations.timeoutDefault",
                { ms: target.timeout_ms },
              )}
              style={{ width: "100%" }}
            />
          </Form.Item>

          {dangerous && (
            <StepUpCard
              autoStart
              action={`edge_operation:invoke:${target.operation}`}
              details={{ gateway_id: gatewayId }}
              labels={{
                start: t("pages.deviceDetail.operations.startStepUp"),
                codePlaceholder: t(
                  "pages.deviceDetail.operations.codePlaceholder",
                ),
                verify: t("pages.deviceDetail.operations.verifyCode"),
                verified: t("pages.deviceDetail.operations.stepUpVerified"),
              }}
              onVerified={setStepUpId}
            />
          )}
        </Form>
      )}
    </Modal>
  );
}

interface OperationRecordDrawerProps {
  // record is the persisted invocation to inspect.
  record: OperationTask;
  // onClose dismisses the drawer.
  onClose: () => void;
}

// prettyJSON formats a value for read-only display.
function prettyJSON(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

// OperationRecordDrawer shows one persisted task in full: metadata, params,
// the edge result and any error reason. The list endpoint already returns
// every field, so this is purely a read-only presentation of it.
function OperationRecordDrawer({
  record,
  onClose,
}: OperationRecordDrawerProps) {
  const { t } = useTranslation();

  return (
    <Drawer
      open
      width={560}
      onClose={onClose}
      title={t("pages.deviceDetail.operations.recordTitle")}
    >
      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label={t("pages.deviceDetail.operations.columns.operation")}>
          <Typography.Text code>{record.operation}</Typography.Text>
        </Descriptions.Item>
        {record.type_version && (
          <Descriptions.Item label={t("pages.deviceDetail.operations.recordVersion")}>
            {record.type_version}
          </Descriptions.Item>
        )}
        <Descriptions.Item label={t("pages.deviceDetail.operations.columns.risk")}>
          <Tag color={riskColor(record.risk)}>
            {t(`pages.deviceDetail.operations.risks.${record.risk}`)}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.operations.columns.status")}>
          <Tag color={taskStatusColor(record.status)}>
            {t(`pages.deviceDetail.operations.statuses.${record.status}`)}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.operations.columns.started")}>
          {record.started_at}
        </Descriptions.Item>
        {record.finished_at && (
          <Descriptions.Item label={t("pages.deviceDetail.operations.recordFinished")}>
            {record.finished_at}
          </Descriptions.Item>
        )}
        <Descriptions.Item label={t("pages.deviceDetail.operations.columns.operator")}>
          {record.invoked_by_email || record.invoked_by}
        </Descriptions.Item>
      </Descriptions>

      {record.reason && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 16 }}
          message={t("pages.deviceDetail.operations.recordReason")}
          description={<pre style={{ margin: 0, whiteSpace: "pre-wrap" }}>{record.reason}</pre>}
        />
      )}

      <Card size="small" title={t("pages.deviceDetail.operations.recordParams")} style={{ marginTop: 16 }}>
        <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
          {prettyJSON(record.params)}
        </pre>
      </Card>

      {record.result != null && (
        <Card size="small" title={t("pages.deviceDetail.operations.recordResult")} style={{ marginTop: 16 }}>
          <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
            {prettyJSON(record.result)}
          </pre>
        </Card>
      )}
    </Drawer>
  );
}

