// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SystemCAsPage is the platform trust-anchor console for device mTLS
// (ADR-0009): list the built-in and imported CAs, import a third-party
// CA, enable/disable trust and delete imported anchors. Every action is
// platform-admin only; the TLS handshake pool reads changes live.

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
  Table,
  Tag,
  Typography,
} from "antd";
import { DownloadOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import dayjs from "dayjs";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";
import {
  deleteTrustAnchor,
  importTrustAnchor,
  listTrustAnchors,
  setTrustAnchorStatus,
  type TrustAnchor,
} from "../api/ca";
import { downloadText } from "../utils/download";
import { formatDate } from "../utils/format";
import { Alert } from "antd";

// SystemCAsPage renders the trust-anchor table and import drawer.

// caExpiryWarnDays flags anchors expiring within this window.
const caExpiryWarnDays = 30;

export default function SystemCAsPage() {
  const { t } = useTranslation();
  const user = authStore((state) => state.user);
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const [importOpen, setImportOpen] = useState(false);
  const [importForm] = Form.useForm<{ label: string; pem: string }>();
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [builtinFilter, setBuiltinFilter] = useState("");

  const query = useQuery({ queryKey: ["platform-cas"], queryFn: listTrustAnchors });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const anchors = useMemo(() => query.data ?? [], [query.data]);

  // filteredAnchors applies client-side keyword search over label and
  // subject CN plus status and built-in/imported enum filters; empty
  // filter values mean all.
  const filteredAnchors = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return anchors.filter((anchor) => {
      if (statusFilter && anchor.status !== statusFilter) {
        return false;
      }
      if (
        builtinFilter &&
        (builtinFilter === "builtin") !== anchor.builtin
      ) {
        return false;
      }
      if (!needle) return true;
      return (
        anchor.label.toLowerCase().includes(needle) ||
        anchor.subject_cn.toLowerCase().includes(needle)
      );
    });
  }, [anchors, keyword, statusFilter, builtinFilter]);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["platform-cas"] });

  const importMutation = useMutation({
    mutationFn: (values: { label: string; pem: string }) =>
      importTrustAnchor(values.label, values.pem),
    onSuccess: async () => {
      await invalidate();
      setImportOpen(false);
      message.success(t("pages.systemCAs.toasts.imported"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const statusMutation = useMutation({
    mutationFn: (args: { id: string; status: "ACTIVE" | "DISABLED" }) =>
      setTrustAnchorStatus(args.id, args.status),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.systemCAs.toasts.statusChanged"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteTrustAnchor,
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.systemCAs.toasts.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  if (!user?.is_platform_admin) {
    return (
      <>
        <PageHeader title={t("pages.systemCAs.title")} />
        <Alert type="error" showIcon message={t("pages.systemCAs.forbidden")} />
      </>
    );
  }

  const columns = [
    {
      title: t("pages.systemCAs.columns.label"),
      dataIndex: "label",
      render: (label: string, record: TrustAnchor) => (
        <Space size={4}>
          {label}
          {record.builtin && <Tag color="blue">{t("pages.systemCAs.builtinTag")}</Tag>}
        </Space>
      ),
    },
    {
      title: t("pages.systemCAs.columns.subject"),
      dataIndex: "subject_cn",
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.systemCAs.columns.issuer"),
      dataIndex: "issuer_cn",
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.systemCAs.columns.fingerprint"),
      dataIndex: "fingerprint_sha256",
      render: (value: string) => (
        <Typography.Text code copyable style={{ fontSize: 12 }}>
          {value.slice(0, 19)}…
        </Typography.Text>
      ),
    },
    {
      title: t("pages.systemCAs.columns.expires"),
      dataIndex: "not_after",
      render: (value: string) => {
        if (!value) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        const now = dayjs();
        const end = dayjs(value);
        const days = end.diff(now, "day");
        const state =
          end.isBefore(now)
            ? "expired"
            : days < caExpiryWarnDays
              ? "expiring"
              : "valid";
        const color = state === "expired" ? "error" : state === "expiring" ? "warning" : "success";
        return (
          <Space direction="vertical" size={0}>
            <span>{formatDate(value)}</span>
            <Tag color={color} style={{ marginInlineEnd: 0, width: "fit-content" }}>
              {t(`pages.systemCAs.validity.${state}`, {
                days: Math.max(days, 0),
              })}
            </Tag>
          </Space>
        );
      },
    },
    {
      title: t("pages.systemCAs.columns.status"),
      dataIndex: "status",
      width: 110,
      render: (status: string) => (
        <Tag color={status === "ACTIVE" ? "green" : "default"}>
          {t(`pages.systemCAs.statuses.${status}`)}
        </Tag>
      ),
    },
    {
      title: t("pages.systemCAs.columns.actions"),
      key: "actions",
      width: 250,
      render: (_: unknown, record: TrustAnchor) => (
        <Space size={4} wrap>
          <Button
            size="small"
            icon={<DownloadOutlined />}
            onClick={() => downloadText(`${record.label}.pem`, record.pem)}
          >
            {t("pages.systemCAs.download")}
          </Button>
          {record.status === "ACTIVE" ? (
            <Button
              size="small"
              onClick={() =>
                statusMutation.mutate({ id: record.id, status: "DISABLED" })
              }
            >
              {t("pages.systemCAs.disable")}
            </Button>
          ) : (
            <Button
              size="small"
              onClick={() =>
                statusMutation.mutate({ id: record.id, status: "ACTIVE" })
              }
            >
              {t("pages.systemCAs.enable")}
            </Button>
          )}
          {!record.builtin && (
            <Popconfirm
              title={t("pages.systemCAs.deleteTitle")}
              onConfirm={() => deleteMutation.mutate(record.id)}
            >
              <Button size="small" danger>
                {t("common.delete")}
              </Button>
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.systemCAs.title")}
        subtitle={t("pages.systemCAs.subtitle")}
        helpId="system-cas"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              importForm.resetFields();
              setImportOpen(true);
            }}
          >
            {t("pages.systemCAs.import")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.systemCAs.filters.searchPlaceholder")}
          filters={[
            {
              key: "status",
              placeholder: t("pages.systemCAs.filters.statusAll"),
              value: statusFilter,
              onChange: setStatusFilter,
              options: [
                { value: "ACTIVE", label: t("pages.systemCAs.statuses.ACTIVE") },
                { value: "DISABLED", label: t("pages.systemCAs.statuses.DISABLED") },
              ],
            },
            {
              key: "builtin",
              placeholder: t("pages.systemCAs.filters.originAll"),
              value: builtinFilter,
              onChange: setBuiltinFilter,
              options: [
                { value: "builtin", label: t("pages.systemCAs.filters.builtin") },
                { value: "imported", label: t("pages.systemCAs.filters.imported") },
              ],
            },
          ]}
        />
        <Table
          rowKey="id"
          loading={query.isFetching}
          columns={columns}
          dataSource={filteredAnchors}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <Drawer
        open={importOpen}
        title={t("pages.systemCAs.importTitle")}
        width={720}
        onClose={() => setImportOpen(false)}
        footer={null}
        destroyOnHidden
      >
        <Form
          form={importForm}
          layout="vertical"
          preserve={false}
          onFinish={(values) => importMutation.mutate(values)}
        >
          <Form.Item
            label={t("pages.systemCAs.form.label")}
            name="label"
            rules={[
              {
                required: true,
                message: t("pages.systemCAs.form.labelRequired"),
              },
            ]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            label={t("pages.systemCAs.form.pem")}
            name="pem"
            tooltip={t("pages.systemCAs.form.pemTip")}
            rules={[
              {
                required: true,
                message: t("pages.systemCAs.form.pemRequired"),
              },
            ]}
          >
            <Input.TextArea rows={10} />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setImportOpen(false)}>{t("common.cancel")}</Button>
              <Button type="primary" htmlType="submit" loading={importMutation.isPending}>
                {t("common.save")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
}
