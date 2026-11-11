// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CompanionEndpointsPage administers the platform-neutral companion
// connector (companion-subscription/v1). Endpoints are the only attachment
// point for external companion services: registration plus conformance, not
// per-application NL code. The page is REST-only; subscription delivery is
// driven by business orchestration deployment.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { formatDateTime } from "../utils/format";
import {
  createCompanionEndpoint,
  deleteCompanionEndpoint,
  listCompanionEndpoints,
  listCompanionSubscriptions,
  updateCompanionEndpoint,
  type CompanionEndpoint,
  type CompanionEndpointInput,
} from "../api/companions";

interface FormValues {
  companion_id: string;
  gateway_id: string;
  base_url: string;
  auth_token?: string;
  insecure_skip_verify: boolean;
  enabled: boolean;
}

// CompanionEndpointsPage renders the endpoint management page.
export default function CompanionEndpointsPage() {
  const { t } = useTranslation();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<CompanionEndpoint | undefined>();
  const [keyword, setKeyword] = useState("");

  const query = useQuery({
    queryKey: ["companion-endpoints"],
    queryFn: listCompanionEndpoints,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const endpoints = useMemo(() => query.data ?? [], [query.data]);

  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    if (!needle) return endpoints;
    return endpoints.filter(
      (ep) =>
        ep.companion_id.toLowerCase().includes(needle) ||
        ep.gateway_id.toLowerCase().includes(needle) ||
        ep.base_url.toLowerCase().includes(needle),
    );
  }, [endpoints, keyword]);

  const openCreate = () => {
    setEditing(undefined);
    setDrawerOpen(true);
  };
  const openEdit = (ep: CompanionEndpoint) => {
    setEditing(ep);
    setDrawerOpen(true);
  };

  return (
    <>
      <PageHeader
        title={t("pages.companionEndpoints.title")}
        subtitle={t("pages.companionEndpoints.subtitle")}
        helpId="companion-endpoints"
      />
      <Card>
        <div style={{ position: "relative" }}>
          <Button
            style={{ position: "absolute", left: 0, top: 4, zIndex: 1 }}
            type="primary"
            icon={<PlusOutlined />}
            onClick={openCreate}
          >
            {t("pages.companionEndpoints.create")}
          </Button>
          <FilterSearchBar
            searchValue={keyword}
            onSearchChange={setKeyword}
            searchPlaceholder={t(
              "pages.companionEndpoints.filters.searchPlaceholder",
            )}
            filters={[]}
          />
        </div>
        <EndpointTable
          endpoints={filtered}
          loading={query.isFetching}
          onEdit={openEdit}
        />
      </Card>
      <EndpointFormDrawer
        open={drawerOpen}
        endpoint={editing}
        onClose={() => setDrawerOpen(false)}
      />
    </>
  );
}

// EndpointTable renders the endpoint list with edit, delete and read-back.
function EndpointTable({
  endpoints,
  loading,
  onEdit,
}: {
  endpoints: CompanionEndpoint[];
  loading: boolean;
  onEdit: (ep: CompanionEndpoint) => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [readback, setReadback] = useState<CompanionEndpoint | undefined>();

  const deleteMutation = useMutation({
    mutationFn: deleteCompanionEndpoint,
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["companion-endpoints"],
      });
      message.success(t("pages.companionEndpoints.toasts.deleted"));
    },
    onError: (error) => message.error(String(error)),
  });

  const columns = [
    {
      title: t("pages.companionEndpoints.columns.companion"),
      dataIndex: "companion_id",
      width: 180,
      render: (id: string) => <Tag color="geekblue">{id}</Tag>,
    },
    { title: t("pages.companionEndpoints.columns.gateway"), dataIndex: "gateway_id", width: 160 },
    {
      title: t("pages.companionEndpoints.columns.baseUrl"),
      dataIndex: "base_url",
      ellipsis: true,
      render: (url: string) => (
        <Tooltip title={url}>
          <span>{url}</span>
        </Tooltip>
      ),
    },
    {
      title: t("pages.companionEndpoints.columns.contract"),
      dataIndex: "contract_version",
      width: 210,
    },
    {
      title: t("pages.companionEndpoints.columns.token"),
      dataIndex: "auth_token_set",
      width: 110,
      render: (set: boolean) =>
        set ? (
          <Tag color="green">{t("pages.companionEndpoints.tokenSet")}</Tag>
        ) : (
          <Tag>{t("pages.companionEndpoints.tokenAbsent")}</Tag>
        ),
    },
    {
      title: t("pages.companionEndpoints.columns.updatedAt"),
      dataIndex: "updated_at",
      width: 180,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("common.enabled"),
      dataIndex: "enabled",
      width: 90,
      render: (enabled: boolean) => <Switch checked={enabled} size="small" disabled />,
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 130,
      render: (_: unknown, ep: CompanionEndpoint) => (
        <Space>
          <Tooltip title={t("pages.companionEndpoints.subscriptions.title")}>
            <Button
              type="text"
              size="small"
              icon={<EyeOutlined />}
              onClick={() => setReadback(ep)}
            />
          </Tooltip>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => onEdit(ep)}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.companionEndpoints.confirm.title")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(ep.id)}
          >
            <Tooltip title={t("common.delete")}>
              <Button type="text" size="small" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      <Table
        rowKey="id"
        loading={loading}
        columns={columns}
        dataSource={endpoints}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
        }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <ReadbackDrawer endpoint={readback} onClose={() => setReadback(undefined)} />
    </>
  );
}

// EndpointFormDrawer renders the create/edit form. The natural key is
// immutable on edit; the token field distinguishes keep/set/clear.
function EndpointFormDrawer({
  open,
  endpoint,
  onClose,
}: {
  open: boolean;
  endpoint?: CompanionEndpoint;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [form] = Form.useForm<FormValues>();

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      const input: CompanionEndpointInput = {
        companion_id: values.companion_id,
        gateway_id: values.gateway_id,
        base_url: values.base_url,
        insecure_skip_verify: values.insecure_skip_verify,
        enabled: values.enabled,
      };
      // On create the token is required; on edit undefined keeps the stored
      // secret and an empty string clears it.
      if (!endpoint || values.auth_token !== undefined) {
        input.auth_token = values.auth_token ?? "";
      }
      if (endpoint) {
        return updateCompanionEndpoint(endpoint.id, input);
      }
      return createCompanionEndpoint(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["companion-endpoints"] });
      message.success(
        t(
          endpoint
            ? "pages.companionEndpoints.toasts.updated"
            : "pages.companionEndpoints.toasts.created",
        ),
      );
      onClose();
    },
    onError: (error) => message.error(String(error)),
  });

  return (
    <Drawer
      title={t(
        endpoint
          ? "pages.companionEndpoints.editTitle"
          : "pages.companionEndpoints.createTitle",
      )}
      width={480}
      open={open}
      onClose={onClose}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            loading={mutation.isPending}
            onClick={() => form.submit()}
          >
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{
          insecure_skip_verify: false,
          enabled: true,
          ...(endpoint
            ? {
                companion_id: endpoint.companion_id,
                gateway_id: endpoint.gateway_id,
                base_url: endpoint.base_url,
                insecure_skip_verify: endpoint.insecure_skip_verify,
                enabled: endpoint.enabled,
              }
            : {}),
        }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.companionEndpoints.form.companionId")}
          name="companion_id"
          rules={[
            { required: true, message: t("pages.companionEndpoints.form.required") },
            { pattern: /^[a-z0-9][a-z0-9._-]{0,127}$/, message: t("pages.companionEndpoints.form.slugHint") },
          ]}
        >
          <Input disabled={!!endpoint} />
        </Form.Item>
        <Form.Item
          label={t("pages.companionEndpoints.form.gatewayId")}
          name="gateway_id"
          rules={[
            { required: true, message: t("pages.companionEndpoints.form.required") },
            { pattern: /^[a-z0-9][a-z0-9._-]{0,127}$/, message: t("pages.companionEndpoints.form.slugHint") },
          ]}
        >
          <Input disabled={!!endpoint} />
        </Form.Item>
        <Form.Item
          label={t("pages.companionEndpoints.form.baseUrl")}
          name="base_url"
          extra={t("pages.companionEndpoints.form.baseUrlHint")}
          rules={[
            { required: true, message: t("pages.companionEndpoints.form.required") },
            { type: "url", message: t("pages.companionEndpoints.form.urlHint") },
          ]}
        >
          <Input placeholder="https://companion.example" />
        </Form.Item>
        <Form.Item
          label={t("pages.companionEndpoints.form.authToken")}
          name="auth_token"
          extra={t(
            endpoint
              ? "pages.companionEndpoints.form.authTokenHintEdit"
              : "pages.companionEndpoints.form.authTokenHintCreate",
          )}
          rules={
            endpoint
              ? undefined
              : [{ required: true, message: t("pages.companionEndpoints.form.required") }]
          }
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item
          label={t("pages.companionEndpoints.form.insecureSkipVerify")}
          name="insecure_skip_verify"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>
        <Form.Item
          label={t("common.enabled")}
          name="enabled"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// statusColor maps read-back status to a tag color.
function statusColor(status: string): string {
  switch (status) {
    case "APPLIED":
      return "success";
    case "FAILED":
      return "error";
    default:
      return "warning";
  }
}

// ReadbackDrawer shows the desired subscription rows and their last
// companion-reported status.
function ReadbackDrawer({
  endpoint,
  onClose,
}: {
  endpoint?: CompanionEndpoint;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: ["companion-subscriptions", endpoint?.id],
    queryFn: () => listCompanionSubscriptions(endpoint!.id),
    enabled: !!endpoint,
  });

  const columns = [
    { title: t("pages.companionEndpoints.subscriptions.sourceDevice"), dataIndex: "source_device", width: 160 },
    { title: t("pages.companionEndpoints.subscriptions.pipeline"), dataIndex: "pipeline_id", width: 170 },
    {
      title: t("pages.companionEndpoints.subscriptions.conclusionDevice"),
      dataIndex: "conclusion_device_id",
      width: 200,
    },
    {
      title: t("pages.companionEndpoints.subscriptions.status"),
      dataIndex: "status",
      width: 110,
      render: (status: string) => (
        <Tag color={statusColor(status)}>
          {t(`pages.companionEndpoints.status.${status}`)}
        </Tag>
      ),
    },
    { title: t("pages.companionEndpoints.subscriptions.revision"), dataIndex: "revision", width: 90 },
    {
      title: t("pages.companionEndpoints.subscriptions.reason"),
      dataIndex: "reason",
      render: (reason: string) => reason || "-",
    },
  ];

  return (
    <Drawer
      title={t("pages.companionEndpoints.subscriptions.title")}
      width={860}
      open={!!endpoint}
      onClose={onClose}
    >
      <Table
        rowKey="id"
        loading={query.isFetching}
        columns={columns}
        dataSource={query.data ?? []}
        pagination={false}
        locale={{ emptyText: t("pages.companionEndpoints.subscriptions.empty") }}
      />
    </Drawer>
  );
}
