// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SystemTenantsPage is the platform console for tenants and their
// administrators (ADR-0021/0022). Platform operators create and
// provision tenants, suspend or resume them, retry failed tasks and
// manage each tenant's member accounts, roles and credentials.

import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Drawer,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  type FormInstance,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import MembersPanel from "../components/MembersPanel";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";
import {
  createTenant,
  listTenants,
  resumeTenant,
  retryTenant,
  suspendTenant,
  type ProvisionTask,
} from "../api/system";
import { getTenantQuota, updateTenantQuota, type TenantQuota } from "../api/quotas";

// taskStatusColors keeps provisioning state visually stable.
const taskStatusColors: Record<string, string> = {
  ACTIVE: "green",
  FAILED: "red",
  PROVISIONING: "orange",
};

// SystemTenantsPage renders the platform administration page.
export default function SystemTenantsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const user = authStore((state) => state.user);

  const [createOpen, setCreateOpen] = useState(false);
  const [membersTenant, setMembersTenant] = useState<ProvisionTask | undefined>();
  const [suspending, setSuspending] = useState<ProvisionTask | undefined>();
  const [quotaTenant, setQuotaTenant] = useState<ProvisionTask | undefined>();
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState<string>("");

  const tenantsQuery = useQuery({
    queryKey: ["system-tenants"],
    queryFn: listTenants,
    refetchInterval: 5000,
  });
  // Memoized so derived row state keeps a stable dependency identity
  // instead of seeing a fresh [] fallback on every render.
  const tenants = useMemo(() => tenantsQuery.data ?? [], [tenantsQuery.data]);

  // tenantRowState classifies a row for the status filter, combining the
  // provisioning task status with the tenant lifecycle flag (a provisioned
  // tenant may still be suspended).
  const tenantRowState = (row: ProvisionTask): string => {
    if (row.tenant_status === "SUSPENDED") return "suspended";
    if (row.status === "FAILED") return "failed";
    if (row.status === "PROVISIONING") return "provisioning";
    return "active";
  };

  const filteredTenants = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return tenants.filter((row) => {
      if (statusFilter && tenantRowState(row) !== statusFilter) {
        return false;
      }
      if (!needle) return true;
      return (
        row.tenant_name.toLowerCase().includes(needle) ||
        row.tenant_id.toLowerCase().includes(needle)
      );
    });
  }, [tenants, keyword, statusFilter]);

  const invalidateTenants = () =>
    queryClient.invalidateQueries({ queryKey: ["system-tenants"] });

  const onError = (error: unknown) => message.error(extractError(error).message);

  const createTenantMutation = useMutation({
    mutationFn: (name: string) => createTenant(name),
    onSuccess: async () => {
      await invalidateTenants();
      message.success(t("pages.systemTenants.toasts.tenantCreated"));
      setCreateOpen(false);
    },
    onError,
  });

  const retryMutation = useMutation({
    mutationFn: (tenantID: string) => retryTenant(tenantID),
    onSuccess: async () => {
      await invalidateTenants();
      message.success(t("pages.systemTenants.toasts.retried"));
    },
    onError,
  });

  const suspendMutation = useMutation({
    mutationFn: (args: { tenantID: string; reason: string }) =>
      suspendTenant(args.tenantID, args.reason),
    onSuccess: async () => {
      await invalidateTenants();
      message.success(t("pages.systemTenants.toasts.suspended"));
      setSuspending(undefined);
    },
    onError,
  });

  const resumeMutation = useMutation({
    mutationFn: (tenantID: string) => resumeTenant(tenantID),
    onSuccess: async () => {
      await invalidateTenants();
      message.success(t("pages.systemTenants.toasts.resumed"));
    },
    onError,
  });

  if (!user?.is_platform_admin) {
    return (
      <>
        <PageHeader title={t("pages.systemTenants.title")} />
        <Alert type="error" showIcon message={t("pages.systemTenants.forbidden")} />
      </>
    );
  }

  const tenantColumns = [
    {
      title: t("pages.systemTenants.columns.name"),
      dataIndex: "tenant_name",
    },
    {
      title: t("pages.systemTenants.columns.id"),
      dataIndex: "tenant_id",
      width: 240,
      render: (id: string) => (
        <Tooltip title={id}>
          <code>{id.slice(0, 13)}...</code>
        </Tooltip>
      ),
    },
    {
      title: t("pages.systemTenants.columns.status"),
      key: "status",
      width: 200,
      render: (_: unknown, row: ProvisionTask) => (
        <Space size={4} wrap>
          <Tag color={taskStatusColors[row.status]}>
            {t(`pages.systemTenants.taskStatus.${row.status}`, {
              defaultValue: row.status,
            })}
          </Tag>
          {row.tenant_status === "SUSPENDED" && (
            <Tag color="volcano">
              {t("pages.systemTenants.taskStatus.SUSPENDED")}
            </Tag>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.systemTenants.columns.attempts"),
      dataIndex: "attempts",
      width: 90,
    },
    {
      title: t("pages.systemTenants.columns.lastError"),
      dataIndex: "last_error",
      width: 200,
      render: (value: string) =>
        value ? (
          <Tooltip title={value}>
            <span>{value}</span>
          </Tooltip>
        ) : (
          "-"
        ),
    },
    {
      title: t("pages.systemTenants.columns.actions"),
      key: "actions",
      width: 300,
      render: (_: unknown, row: ProvisionTask) => (
        <Space size={4} wrap>
          <Button
            size="small"
            type="primary"
            onClick={() => setMembersTenant(row)}
          >
            {t("pages.systemTenants.actions.members")}
          </Button>
          <Button size="small" onClick={() => setQuotaTenant(row)}>
            {t("pages.systemTenants.actions.quota")}
          </Button>
          {row.status === "FAILED" && (
            <Button
              size="small"
              icon={<ReloadOutlined />}
              onClick={() => retryMutation.mutate(row.tenant_id)}
            >
              {t("pages.systemTenants.actions.retry")}
            </Button>
          )}
          {row.tenant_status === "SUSPENDED" ? (
            <Popconfirm
              title={t("pages.systemTenants.confirm.resume")}
              onConfirm={() => resumeMutation.mutate(row.tenant_id)}
            >
              <Button size="small">{t("pages.systemTenants.actions.resume")}</Button>
            </Popconfirm>
          ) : (
            <Button size="small" danger onClick={() => setSuspending(row)}>
              {t("pages.systemTenants.actions.suspend")}
            </Button>
          )}
        </Space>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.systemTenants.title")}
        subtitle={t("pages.systemTenants.subtitle")}
        helpId="system-tenants"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setCreateOpen(true)}
          >
            {t("pages.systemTenants.actions.create")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.systemTenants.filters.searchPlaceholder")}
          filters={[
            {
              key: "status",
              placeholder: t("pages.systemTenants.filters.statusAll"),
              value: statusFilter,
              onChange: setStatusFilter,
              options: [
                { value: "active", label: t("pages.systemTenants.taskStatus.ACTIVE") },
                { value: "provisioning", label: t("pages.systemTenants.taskStatus.PROVISIONING") },
                { value: "failed", label: t("pages.systemTenants.taskStatus.FAILED") },
                { value: "suspended", label: t("pages.systemTenants.taskStatus.SUSPENDED") },
              ],
            },
          ]}
        />
        <Table
          rowKey={(row) => row.tenant_id}
          loading={tenantsQuery.isFetching}
          columns={tenantColumns}
          dataSource={filteredTenants}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("pages.systemTenants.filters.noResults") }}
        />
      </Card>

      <CreateTenantModal
        open={createOpen}
        loading={createTenantMutation.isPending}
        onCancel={() => setCreateOpen(false)}
        onSubmit={(name) => createTenantMutation.mutate(name)}
      />

      <SuspendModal
        tenant={suspending}
        loading={suspendMutation.isPending}
        onCancel={() => setSuspending(undefined)}
        onSubmit={(reason) =>
          suspending &&
          suspendMutation.mutate({ tenantID: suspending.tenant_id, reason })
        }
      />

      <MembersDrawer
        tenant={membersTenant}
        onClose={() => setMembersTenant(undefined)}
      />

      <QuotaDrawer
        tenant={quotaTenant}
        onClose={() => setQuotaTenant(undefined)}
      />
    </>
  );
}

// CreateTenantValues is the create form shape.
interface CreateTenantValues {
  name: string;
}

// CreateTenantModal collects the display name of a new tenant inside a drawer.
function CreateTenantModal({
  open,
  loading,
  onCancel,
  onSubmit,
}: {
  open: boolean;
  loading: boolean;
  onCancel: () => void;
  onSubmit: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<CreateTenantValues>();
  return (
    <Drawer
      open={open}
      title={t("pages.systemTenants.createTitle")}
      width={720}
      onClose={onCancel}
      destroyOnHidden
      footer={null}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => onSubmit(values.name)}
      >
        <Form.Item
          label={t("pages.systemTenants.fields.name")}
          name="name"
          rules={[
            {
              required: true,
              message: t("pages.systemTenants.fields.nameRequired"),
            },
          ]}
        >
          <Input />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={loading}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// SuspendValues is the suspension form shape.
interface SuspendValues {
  reason: string;
}

// SuspendModal collects the reason before suspending a tenant inside a drawer.
function SuspendModal({
  tenant,
  loading,
  onCancel,
  onSubmit,
}: {
  tenant?: ProvisionTask;
  loading: boolean;
  onCancel: () => void;
  onSubmit: (reason: string) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<SuspendValues>();
  return (
    <Drawer
      open={!!tenant}
      title={t("pages.systemTenants.suspendTitle")}
      width={720}
      onClose={onCancel}
      destroyOnHidden
      footer={null}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => onSubmit(values.reason)}
      >
        <Form.Item label={t("pages.systemTenants.fields.tenant")}>
          <Input value={tenant?.tenant_name} disabled />
        </Form.Item>
        <Form.Item
          label={t("pages.systemTenants.fields.reason")}
          name="reason"
          rules={[
            {
              required: true,
              message: t("pages.systemTenants.fields.reasonRequired"),
            },
          ]}
        >
          <Input.TextArea rows={3} />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button type="primary" danger htmlType="submit" loading={loading}>
              {t("pages.systemTenants.actions.suspend")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// QuotaFormValues is the quota editor shape.
interface QuotaFormValues {
  device_limit: number;
  ingest_rate_limit: number;
  ws_subscription_limit: number;
}

// QuotaDrawer edits the soft quotas of one tenant. Values load from the
// effective quota; 0 means unlimited.
function QuotaDrawer({
  tenant,
  onClose,
}: {
  tenant?: ProvisionTask;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<QuotaFormValues>();
  const { message } = AntApp.useApp();

  const quotaQuery = useQuery({
    queryKey: ["tenant-quota", tenant?.tenant_id],
    queryFn: () => getTenantQuota(tenant!.tenant_id),
    enabled: !!tenant,
  });

  const updateMutation = useMutation({
    mutationFn: (values: QuotaFormValues) =>
      updateTenantQuota(tenant!.tenant_id, values),
    onSuccess: () => message.success(t("pages.systemTenants.quota.saved")),
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={!!tenant}
      width={480}
      title={`${t("pages.systemTenants.quota.title")} — ${tenant?.tenant_name ?? ""}`}
      onClose={onClose}
      destroyOnHidden
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => updateMutation.mutate(values)}
      >
        <Space wrap>
          <Form.Item label={t("pages.systemTenants.quota.deviceLimit")} name="device_limit">
            <InputNumber min={0} style={{ width: 180 }} />
          </Form.Item>
          <Form.Item label={t("pages.systemTenants.quota.ingestLimit")} name="ingest_rate_limit">
            <InputNumber min={0} style={{ width: 180 }} />
          </Form.Item>
          <Form.Item label={t("pages.systemTenants.quota.wsLimit")} name="ws_subscription_limit">
            <InputNumber min={0} style={{ width: 180 }} />
          </Form.Item>
        </Space>
        <p>{t("pages.systemTenants.quota.zeroHint")}</p>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={updateMutation.isPending}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>

      {quotaQuery.data && (
        <QuotaDefaults data={quotaQuery.data} form={form} />
      )}
    </Drawer>
  );
}

// QuotaDefaults pushes loaded quota values into the form once. A small
// component keeps the effect below the Form and avoids a render-phase
// setFieldsValue call.
function QuotaDefaults({
  data,
  form,
}: {
  data: TenantQuota;
  form: FormInstance<QuotaFormValues>;
}) {
  useEffect(() => {
    form.setFieldsValue({
      device_limit: data.device_limit,
      ingest_rate_limit: data.ingest_rate_limit,
      ws_subscription_limit: data.ws_subscription_limit,
    });
  }, [data, form]);
  return null;
}

// MembersDrawer hosts the reusable member panel for one tenant.
function MembersDrawer({
  tenant,
  onClose,
}: {
  tenant?: ProvisionTask;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Drawer
      open={!!tenant}
      width={1100}
      title={`${t("pages.systemTenants.members.title")} — ${tenant?.tenant_name ?? ""}`}
      onClose={onClose}
      destroyOnHidden
    >
      {tenant && <MembersPanel tenantId={tenant.tenant_id} />}
    </Drawer>
  );
}
