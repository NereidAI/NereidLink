// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// MembersPanel is the reusable tenant member administration surface
// (ADR-0021/0022): it lists the members of one tenant and supports
// account creation with a one-time password, role replacement, password
// reset, enable/disable and removal. It is embedded both in the tenant
// drawer of the platform console and in the standalone Users & Roles
// page. The backing routes admit platform admins (any tenant) and a
// TENANT_ADMIN inside their own tenant.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
} from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import {
  assignableDataScopes,
  assignableRoles,
  createMember,
  listMembers,
  removeMember,
  resetMemberPassword,
  setMemberDataScope,
  setMemberEnabled,
  setMemberRoles,
  type DataScope,
  type Member,
} from "../api/system";
import { listCustomers, listDeviceGroups } from "../api/ownership";
import { extractError } from "../api/client";
import FilterSearchBar from "./FilterSearchBar";
import { useWSStore } from "../store/wsStore";

// MemberFormValues is the create member form shape.
interface MemberFormValues {
  email: string;
  display_name?: string;
  password?: string;
  roles: string[];
  data_scope: DataScope;
  data_scope_id?: string;
}

// MembersPanel renders the full member table with its drawers for one
// tenant. The time formatter is provided so the embedding page keeps a
// single locale pattern; pass null to use the panel default.
export default function MembersPanel({ tenantId }: { tenantId: string }) {
  const { t, i18n } = useTranslation();
  const { message, modal } = AntApp.useApp();
  const queryClient = useQueryClient();
  const isZh = i18n.language.startsWith("zh");
  const [createOpen, setCreateOpen] = useState(false);
  const [rolesFor, setRolesFor] = useState<Member | undefined>();
  const [scopeFor, setScopeFor] = useState<Member | undefined>();
  const [passwordFor, setPasswordFor] = useState<Member | undefined>();
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState("");

  // formatTime renders absolute timestamps with the locale pattern.
  const formatTime = (value?: string) =>
    value
      ? dayjs(value).format(isZh ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A")
      : "-";

  const membersQuery = useQuery({
    queryKey: ["members", tenantId],
    queryFn: () => listMembers(tenantId),
    enabled: !!tenantId,
  });
  // Memoized so downstream useMemo dependencies keep a stable identity
  // instead of seeing a fresh [] fallback on every render.
  const members = useMemo(() => membersQuery.data ?? [], [membersQuery.data]);

  // filteredMembers applies client-side keyword search over email and
  // display name plus an enabled/disabled filter from the disabled flag;
  // the member list is unpaged.
  const filteredMembers = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return members.filter((member) => {
      if (statusFilter && (statusFilter === "enabled") === member.disabled) {
        return false;
      }
      if (!needle) return true;
      return (
        member.email.toLowerCase().includes(needle) ||
        member.display_name.toLowerCase().includes(needle)
      );
    });
  }, [members, keyword, statusFilter]);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["members", tenantId] });

  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const memberEvents = useWSStore((state) => state.memberEvents);
  const wsConnected = useWSStore((state) => state.connected);

  // Another platform admin managing members in a parallel session pushes
  // tenant-scoped change-notification frames. The frame only invalidates the
  // list, which is re-read from REST; the tenant is passed explicitly so the
  // same panel works for a platform admin viewing another tenant's drawer.
  useEffect(() => {
    if (!tenantId) return;
    subscribe(`members-${tenantId}`, "member", [], tenantId);
    return () => unsubscribe(`members-${tenantId}`);
  }, [tenantId, subscribe, unsubscribe]);

  useEffect(() => {
    if (memberEvents.length === 0) return;
    const latest = memberEvents[memberEvents.length - 1];
    if (latest.tenant_id !== tenantId) return;
    void invalidate();
    // Only a newly arrived frame should trigger a refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [memberEvents.length]);

  // Missed frames are not replayed, so refetch after a real reconnect.
  const sawDisconnect = useRef(false);
  useEffect(() => {
    if (!wsConnected) {
      sawDisconnect.current = true;
    } else if (sawDisconnect.current) {
      sawDisconnect.current = false;
      void invalidate();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsConnected]);

  const onError = (error: unknown) => message.error(extractError(error).message);

  const createMutation = useMutation({
    mutationFn: (values: MemberFormValues) =>
      createMember(tenantId, {
        email: values.email,
        display_name: values.display_name,
        password: values.password,
        roles: values.roles,
        // TENANT carries no anchor; sending one would be rejected as an
        // invalid scope/anchor pairing.
        data_scope: values.data_scope,
        data_scope_id:
          values.data_scope === "TENANT" ? undefined : values.data_scope_id,
      }),
    onSuccess: async (result) => {
      await invalidate();
      setCreateOpen(false);
      // Generated credentials appear exactly once; surface them with
      // the email so the operator can relay them securely.
      if (result.generated_password) {
        modal.info({
          title: t("pages.systemTenants.members.generatedTitle"),
          content: (
            <Space direction="vertical" style={{ marginTop: 8 }}>
              <span>{result.member.email}</span>
              <Input readOnly value={result.generated_password} />
            </Space>
          ),
        });
      }
    },
    onError,
  });

  const rolesMutation = useMutation({
    mutationFn: (args: { member: Member; roles: string[] }) =>
      setMemberRoles(tenantId, args.member.user_id, args.roles),
    onSuccess: async () => {
      await invalidate();
      setRolesFor(undefined);
    },
    onError,
  });

  const scopeMutation = useMutation({
    mutationFn: (args: {
      member: Member;
      dataScope: DataScope;
      dataScopeId?: string;
    }) =>
      setMemberDataScope(
        tenantId,
        args.member.user_id,
        args.dataScope,
        args.dataScopeId,
      ),
    onSuccess: async () => {
      await invalidate();
      setScopeFor(undefined);
    },
    onError,
  });

  const enabledMutation = useMutation({
    mutationFn: (args: { member: Member; enabled: boolean }) =>
      setMemberEnabled(tenantId, args.member.user_id, args.enabled),
    onSuccess: async () => invalidate(),
    onError,
  });

  const removeMutation = useMutation({
    mutationFn: (member: Member) => removeMember(tenantId, member.user_id),
    onSuccess: async () => invalidate(),
    onError,
  });

  const columns = [
    {
      title: t("pages.systemTenants.members.columns.email"),
      dataIndex: "email",
    },
    {
      title: t("pages.systemTenants.members.columns.name"),
      dataIndex: "display_name",
    },
    {
      title: t("pages.systemTenants.members.columns.roles"),
      dataIndex: "roles",
      render: (roles: string[], member: Member) => (
        <Space size={2} wrap>
          {member.is_platform_admin && (
            <Tag color="gold">PLATFORM_ADMIN</Tag>
          )}
          {roles.map((role) => (
            <Tag key={role}>{role}</Tag>
          ))}
        </Space>
      ),
    },
    {
      title: t("pages.systemTenants.members.columns.dataScope"),
      dataIndex: "data_scope",
      render: (scope: DataScope, member: Member) => (
        <Tooltip
          title={
            scope === "TENANT"
              ? t("pages.systemTenants.members.scopeHint.tenant")
              : member.data_scope_name
                ? `${member.data_scope_name} (${member.data_scope_id ?? ""})`
                : (member.data_scope_id ?? "")
          }
        >
          <Tag color={scope === "TENANT" ? "blue" : "purple"}>
            {scope === "TENANT"
              ? t("pages.systemTenants.members.scope.tenant")
              : `${scope}: ${member.data_scope_name || member.data_scope_id || "?"}`}
          </Tag>
        </Tooltip>
      ),
    },
    {
      title: t("pages.systemTenants.members.columns.status"),
      dataIndex: "disabled",
      width: 100,
      render: (disabled: boolean) =>
        disabled ? (
          <Tag color="default">{t("pages.systemTenants.members.disabled")}</Tag>
        ) : (
          <Tag color="green">{t("pages.systemTenants.members.enabled")}</Tag>
        ),
    },
    {
      title: t("pages.systemTenants.members.columns.lastLogin"),
      dataIndex: "last_login_at",
      width: 190,
      render: (value?: string) => formatTime(value),
    },
    {
      title: t("pages.systemTenants.members.columns.actions"),
      key: "actions",
      width: 280,
      render: (_: unknown, member: Member) => (
        <Space size={4} wrap>
          {member.is_platform_admin && (
            <Button
              size="small"
              onClick={() => setPasswordFor(member)}
            >
              {t("pages.systemTenants.members.actions.password")}
            </Button>
          )}
          {!member.is_platform_admin && (
            <>
              <Button size="small" onClick={() => setRolesFor(member)}>
                {t("pages.systemTenants.members.actions.roles")}
              </Button>
              <Button size="small" onClick={() => setScopeFor(member)}>
                {t("pages.systemTenants.members.actions.scope")}
              </Button>
              <Button
                size="small"
                onClick={() => setPasswordFor(member)}
              >
                {t("pages.systemTenants.members.actions.password")}
              </Button>
              {member.disabled ? (
                <Button
                  size="small"
                  onClick={() => enabledMutation.mutate({ member, enabled: true })}
                >
                  {t("pages.systemTenants.members.actions.enable")}
                </Button>
              ) : (
                <Button
                  size="small"
                  onClick={() => enabledMutation.mutate({ member, enabled: false })}
                >
                  {t("pages.systemTenants.members.actions.disable")}
                </Button>
              )}
              <Popconfirm
                title={t("pages.systemTenants.members.confirm.remove")}
                onConfirm={() => removeMutation.mutate(member)}
              >
                <Button size="small" danger>
                  {t("common.delete")}
                </Button>
              </Popconfirm>
            </>
          )}
        </Space>
      ),
    },
  ];

  return (
    <>
      <div style={{ marginBottom: 12, textAlign: "right" }}>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCreateOpen(true)}
        >
          {t("pages.systemTenants.members.add")}
        </Button>
      </div>
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.systemTenants.members.filters.searchPlaceholder")}
        filters={[
          {
            key: "status",
            placeholder: t("pages.systemTenants.members.filters.statusAll"),
            value: statusFilter,
            onChange: setStatusFilter,
            options: [
              { value: "enabled", label: t("common.filterEnabled") },
              { value: "disabled", label: t("common.filterDisabled") },
            ],
          },
        ]}
      />
      <Table
        rowKey={(row) => row.user_id}
        loading={membersQuery.isLoading}
        columns={columns}
        dataSource={filteredMembers}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />

      <Drawer
        open={createOpen}
        title={t("pages.systemTenants.members.createTitle")}
        width={720}
        onClose={() => setCreateOpen(false)}
        footer={null}
        destroyOnHidden
      >
        <Form<MemberFormValues>
          layout="vertical"
          preserve={false}
          initialValues={{ roles: ["TENANT_ADMIN"] }}
          onFinish={(values) => createMutation.mutate(values)}
        >
          <Form.Item
            label={t("pages.systemTenants.members.fields.email")}
            name="email"
            rules={[
              {
                required: true,
                message: t("pages.systemTenants.members.fields.emailRequired"),
              },
            ]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            label={t("pages.systemTenants.members.fields.name")}
            name="display_name"
          >
            <Input />
          </Form.Item>
          <Form.Item
            label={t("pages.systemTenants.members.fields.password")}
            name="password"
            tooltip={t("pages.systemTenants.members.fields.passwordTooltip")}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            label={t("pages.systemTenants.members.fields.roles")}
            name="roles"
            rules={[{ required: true }]}
          >
            <Select
              mode="multiple"
              options={assignableRoles.map((value) => ({ value, label: value }))}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.systemTenants.members.fields.dataScope")}
            name="data_scope"
            initialValue="TENANT"
            rules={[{ required: true }]}
            extra={t("pages.systemTenants.members.scopeHint.tenant")}
          >
            <Select
              options={assignableDataScopes.map((value) => ({
                value,
                label: t(`pages.systemTenants.members.scope.${value.toLowerCase()}`),
              }))}
            />
          </Form.Item>
          <ScopeAnchorField />
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setCreateOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                type="primary"
                htmlType="submit"
                loading={createMutation.isPending}
              >
                {t("common.save")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>

      <RolesModal
        member={rolesFor}
        onCancel={() => setRolesFor(undefined)}
        onSubmit={(roles) =>
          rolesFor && rolesMutation.mutate({ member: rolesFor, roles })
        }
        loading={rolesMutation.isPending}
      />

      <DataScopeModal
        member={scopeFor}
        onCancel={() => setScopeFor(undefined)}
        onSubmit={(dataScope, dataScopeId) =>
          scopeFor &&
          scopeMutation.mutate({ member: scopeFor, dataScope, dataScopeId })
        }
        loading={scopeMutation.isPending}
      />

      <PasswordResetModal
        member={passwordFor}
        onCancel={() => setPasswordFor(undefined)}
        onSubmit={(newPassword) => {
          const target = passwordFor;
          if (!target) {
            return Promise.resolve();
          }
          return resetMemberPassword(tenantId, target.user_id, newPassword)
            .then((password) => {
              setPasswordFor(undefined);
              modal.info({
                title: t("pages.systemTenants.members.newPasswordTitle"),
                content: (
                  <Space direction="vertical" style={{ marginTop: 8 }}>
                    <span>{target.email}</span>
                    <Input readOnly value={password} />
                  </Space>
                ),
              });
            })
            .catch((err: unknown) => {
              onError(err);
              throw err;
            });
        }}
      />
    </>
  );
}

// RolesModal edits the role set of one member inside a drawer.
function RolesModal({
  member,
  loading,
  onCancel,
  onSubmit,
}: {
  member?: Member;
  loading: boolean;
  onCancel: () => void;
  onSubmit: (roles: string[]) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ roles: string[] }>();
  return (
    <Drawer
      open={!!member}
      title={t("pages.systemTenants.members.rolesTitle")}
      width={720}
      onClose={onCancel}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({ roles: member?.roles ?? [] });
        }
      }}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => onSubmit(values.roles)}
      >
        <Form.Item name="roles" rules={[{ required: true }]}>
          <Select
            mode="multiple"
            options={assignableRoles.map((value) => ({ value, label: value }))}
          />
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

// ScopeAnchorField renders the customer/group anchor picker for the enclosing
// create form, showing it only for a narrowed scope.
//
// It is a sibling of DataScopeModal rather than a shared component because the
// two live in different Form instances and the drawer owns its own level
// watcher; only the anchor control and its option loading are shared.
function ScopeAnchorField() {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const level = Form.useWatch("data_scope", form) as DataScope | undefined;

  const customers = useQuery({
    queryKey: ["customers", "scope-picker"],
    queryFn: listCustomers,
    staleTime: 60_000,
  });
  const groups = useQuery({
    queryKey: ["device-groups", "scope-picker"],
    queryFn: listDeviceGroups,
    staleTime: 60_000,
  });

  if (!level || level === "TENANT") {
    return null;
  }

  const options =
    level === "CUSTOMER"
      ? (customers.data ?? []).map((c) => ({ value: c.id, label: c.name }))
      : (groups.data ?? []).map((g) => ({ value: g.id, label: g.name }));

  return (
    <Form.Item
      label={t("pages.systemTenants.members.fields.dataScopeAnchor")}
      name="data_scope_id"
      rules={[{ required: true }]}
      extra={t(
        level === "CUSTOMER"
          ? "pages.systemTenants.members.scopeHint.customer"
          : "pages.systemTenants.members.scopeHint.group",
      )}
    >
      <Select
        showSearch
        optionFilterProp="label"
        loading={level === "CUSTOMER" ? customers.isFetching : groups.isFetching}
        options={options}
      />
    </Form.Item>
  );
}

// DataScopeValues is the data scope drawer shape. The anchor is only meaningful
// for a narrowed scope, so it is cleared when TENANT is selected instead of
// being left behind as a stale id the server would reject.
interface DataScopeValues {
  data_scope: DataScope;
  data_scope_id?: string;
}

/**
 * DataScopeModal edits one member's data scope inside a drawer.
 *
 * SELF is not offered: ADR-0022 names it, but no resource in the current
 * schema is owned by a user, so the server refuses it. Offering it here would
 * let an operator build a request that can only fail.
 *
 * The anchor picker switches with the level, because a customer id is not a
 * valid group id and the server validates the pairing.
 */
function DataScopeModal({
  member,
  loading,
  onCancel,
  onSubmit,
}: {
  member?: Member;
  loading: boolean;
  onCancel: () => void;
  onSubmit: (dataScope: DataScope, dataScopeId?: string) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<DataScopeValues>();
  const level = Form.useWatch("data_scope", form);

  const customers = useQuery({
    queryKey: ["customers", "scope-picker"],
    queryFn: listCustomers,
    // The picker only needs the id/name pairs; a stale list is still a valid
    // anchor list, and the server re-validates whatever is submitted.
    staleTime: 60_000,
  });
  const groups = useQuery({
    queryKey: ["device-groups", "scope-picker"],
    queryFn: listDeviceGroups,
    staleTime: 60_000,
  });

  const anchorOptions = useMemo(() => {
    if (level === "CUSTOMER") {
      return (customers.data ?? []).map((c) => ({ value: c.id, label: c.name }));
    }
    if (level === "GROUP") {
      return (groups.data ?? []).map((g) => ({ value: g.id, label: g.name }));
    }
    return [];
  }, [level, customers.data, groups.data]);

  return (
    <Drawer
      open={!!member}
      title={t("pages.systemTenants.members.scopeTitle")}
      width={720}
      onClose={onCancel}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            data_scope: member?.data_scope ?? "TENANT",
            data_scope_id: member?.data_scope_id,
          });
        }
      }}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => {
          // TENANT carries no anchor: sending a stale one would be rejected
          // as an invalid pairing.
          onSubmit(
            values.data_scope,
            values.data_scope === "TENANT" ? undefined : values.data_scope_id,
          );
        }}
      >
        <Form.Item
          label={t("pages.systemTenants.members.fields.dataScope")}
          name="data_scope"
          rules={[{ required: true }]}
        >
          <Select
            options={assignableDataScopes.map((value) => ({
              value,
              label: t(`pages.systemTenants.members.scope.${value.toLowerCase()}`),
            }))}
            onChange={() => form.setFieldValue("data_scope_id", undefined)}
          />
        </Form.Item>
        {level && level !== "TENANT" && (
          <Form.Item
            label={t("pages.systemTenants.members.fields.dataScopeAnchor")}
            name="data_scope_id"
            rules={[{ required: true }]}
            extra={t(
              level === "CUSTOMER"
                ? "pages.systemTenants.members.scopeHint.customer"
                : "pages.systemTenants.members.scopeHint.group",
            )}
          >
            <Select
              showSearch
              optionFilterProp="label"
              loading={
                level === "CUSTOMER"
                  ? customers.isFetching
                  : groups.isFetching
              }
              options={anchorOptions}
            />
          </Form.Item>
        )}
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

// PasswordResetValues is the reset drawer shape.
interface PasswordResetValues {
  password?: string;
}

// PasswordResetModal asks for confirmation before changing a credential.
// An empty password asks the server to generate one; the new password is
// shown once after the change.
function PasswordResetModal({
  member,
  onCancel,
  onSubmit,
}: {
  member?: Member;
  onCancel: () => void;
  onSubmit: (newPassword: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<PasswordResetValues>();
  const [submitting, setSubmitting] = useState(false);

  return (
    <Drawer
      open={!!member}
      title={t("pages.systemTenants.members.resetTitle")}
      width={720}
      onClose={onCancel}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (!visible) {
          setSubmitting(false);
        }
      }}
    >
      <Form<PasswordResetValues>
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={async (values) => {
          setSubmitting(true);
          try {
            await onSubmit((values.password ?? "").trim());
          } catch {
            setSubmitting(false);
          }
        }}
      >
        <p style={{ marginTop: 0 }}>
          {t("pages.systemTenants.members.resetWarning", {
            email: member?.email ?? "",
          })}
        </p>
        <Form.Item
          label={t("pages.systemTenants.members.newPasswordLabel")}
          name="password"
          tooltip={t("pages.systemTenants.members.generateTooltip")}
          rules={[
            {
              validator(_, value: string) {
                if (value && value.trim().length > 0 && value.trim().length < 10) {
                  return Promise.reject(
                    new Error(t("pages.systemTenants.members.passwordTooShort")),
                  );
                }
                return Promise.resolve();
              },
            },
          ]}
        >
          <Input.Password
            autoComplete="new-password"
            placeholder={t("pages.systemTenants.members.generatePlaceholder")}
          />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button type="primary" danger htmlType="submit" loading={submitting}>
              {t("pages.systemTenants.members.confirmReset")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}