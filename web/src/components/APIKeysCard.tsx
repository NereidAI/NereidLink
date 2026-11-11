// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

import { useState } from "react";
import {
  App as AntApp,
  Alert,
  Button,
  Card,
  DatePicker,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";

import {
  createAPIKey,
  deleteAPIKey,
  listAPIKeys,
  revokeAPIKey,
  rotateAPIKey,
  type APIKey,
} from "../api/apiKeys";
import { extractError } from "../api/client";
import { formatDateTime } from "../utils/format";

interface CreateFormValues {
  name: string;
  scopes?: string[];
  expiresAt?: dayjs.Dayjs | null;
}

interface RevealedKey {
  title: string;
  value: string;
}

export default function APIKeysCard() {
  const { t } = useTranslation();
  const { message, modal } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [form] = Form.useForm<CreateFormValues>();
  const [createOpen, setCreateOpen] = useState(false);
  const [revealed, setRevealed] = useState<RevealedKey | null>(null);

  const keysQuery = useQuery({
    queryKey: ["user-api-keys"],
    queryFn: listAPIKeys,
  });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["user-api-keys"] });

  const createMutation = useMutation({
    mutationFn: (values: CreateFormValues) =>
      createAPIKey({
        name: values.name,
        scopes: values.scopes ?? [],
        expires_at: values.expiresAt?.toISOString(),
      }),
    onSuccess: async (result) => {
      await invalidate();
      setCreateOpen(false);
      form.resetFields();
      setRevealed({
        title: t("pages.accountSecurity.apiKeys.createdTitle"),
        value: result.api_key,
      });
      message.success(t("pages.accountSecurity.apiKeys.created"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const rotateMutation = useMutation({
    mutationFn: (id: string) => rotateAPIKey(id),
    onSuccess: async (result) => {
      await invalidate();
      setRevealed({
        title: t("pages.accountSecurity.apiKeys.rotatedTitle"),
        value: result.api_key,
      });
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const revokeMutation = useMutation({
    mutationFn: revokeAPIKey,
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.accountSecurity.apiKeys.revokeSuccess"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteAPIKey,
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.accountSecurity.apiKeys.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns: ColumnsType<APIKey> = [
    {
      title: t("pages.accountSecurity.apiKeys.name"),
      dataIndex: "name",
      key: "name",
    },
    {
      title: t("pages.accountSecurity.apiKeys.prefix"),
      dataIndex: "key_prefix",
      key: "key_prefix",
      render: (prefix: string) => <Typography.Text code>{prefix}</Typography.Text>,
    },
    {
      title: t("pages.accountSecurity.apiKeys.scopes"),
      dataIndex: "scopes",
      key: "scopes",
      render: (scopes: string[]) =>
        scopes.length === 0
          ? t("pages.accountSecurity.apiKeys.inherit")
          : scopes.map((scope) => <Tag key={scope}>{scope}</Tag>),
    },
    {
      title: t("pages.accountSecurity.apiKeys.status"),
      dataIndex: "revoked",
      key: "status",
      render: (revoked: boolean, record) => {
        const expired = Boolean(record.expires_at && dayjs(record.expires_at).isBefore(dayjs()));
        if (revoked) return <Tag color="red">{t("pages.accountSecurity.apiKeys.revoked")}</Tag>;
        if (expired) return <Tag color="orange">{t("pages.accountSecurity.apiKeys.expired")}</Tag>;
        return <Tag color="green">{t("pages.accountSecurity.apiKeys.active")}</Tag>;
      },
    },
    {
      title: t("pages.accountSecurity.apiKeys.lastUsed"),
      dataIndex: "last_used_at",
      key: "last_used_at",
      render: (value?: string | null) =>
        value ? formatDateTime(value) : t("pages.accountSecurity.apiKeys.neverUsed"),
    },
    {
      title: t("pages.accountSecurity.apiKeys.actions"),
      key: "actions",
      render: (_, record) => {
        const expired = Boolean(
          record.expires_at && dayjs(record.expires_at).isBefore(dayjs()),
        );
        return (
          <Space>
            <Button
              size="small"
              disabled={record.revoked || expired || rotateMutation.isPending}
              onClick={() =>
                modal.confirm({
                  title: t("pages.accountSecurity.apiKeys.rotateConfirm"),
                  onOk: () => rotateMutation.mutateAsync(record.id),
                })
              }
            >
              {t("pages.accountSecurity.apiKeys.rotate")}
            </Button>
            <Popconfirm
              title={t("pages.accountSecurity.apiKeys.revokeConfirm")}
              onConfirm={() => revokeMutation.mutateAsync(record.id)}
            >
              <Button size="small" danger disabled={record.revoked}>
                {t("pages.accountSecurity.apiKeys.revoke")}
              </Button>
            </Popconfirm>
            <Popconfirm
              title={t("pages.accountSecurity.apiKeys.deleteConfirm")}
              onConfirm={() => deleteMutation.mutateAsync(record.id)}
            >
              <Button size="small" danger type="primary" ghost>
                {t("common.delete")}
              </Button>
            </Popconfirm>
          </Space>
        );
      },
    },
  ];

  return (
    <Card
      title={t("pages.accountSecurity.apiKeys.title")}
      extra={
        <Button type="primary" onClick={() => setCreateOpen(true)}>
          {t("pages.accountSecurity.apiKeys.create")}
        </Button>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph>
          {t("pages.accountSecurity.apiKeys.subtitle")}
        </Typography.Paragraph>
        <Table
          rowKey="id"
          size="small"
          loading={keysQuery.isLoading}
          columns={columns}
          dataSource={keysQuery.data ?? []}
          pagination={false}
        />
      </Space>

      <Drawer
        open={createOpen}
        width={560}
        title={t("pages.accountSecurity.apiKeys.createTitle")}
        onClose={() => setCreateOpen(false)}
        extra={
          <Space>
            <Button onClick={() => setCreateOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              loading={createMutation.isPending}
              onClick={() => form.submit()}
            >
              {t("common.create")}
            </Button>
          </Space>
        }
      >
        <Form
          form={form}
          layout="vertical"
          onFinish={(values) => createMutation.mutate(values)}
        >
          <Form.Item
            label={t("pages.accountSecurity.apiKeys.name")}
            name="name"
            rules={[
              { required: true, message: t("pages.accountSecurity.apiKeys.nameRequired") },
              { max: 200, message: t("pages.accountSecurity.apiKeys.nameMax") },
            ]}
          >
            <Input maxLength={200} />
          </Form.Item>
          <Form.Item
            label={t("pages.accountSecurity.apiKeys.scopesLabel")}
            name="scopes"
            tooltip={t("pages.accountSecurity.apiKeys.scopesHint")}
            extra={t("pages.accountSecurity.apiKeys.scopesExtra")}
          >
            <Select
              mode="tags"
              tokenSeparators={[",", " "]}
              placeholder={t("pages.accountSecurity.apiKeys.scopesPlaceholder")}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.accountSecurity.apiKeys.expiresAt")}
            name="expiresAt"
            extra={t("pages.accountSecurity.apiKeys.expiryHint")}
          >
            <DatePicker showTime style={{ width: "100%" }} />
          </Form.Item>
        </Form>
      </Drawer>

      <Drawer
        open={revealed !== null}
        width={640}
        title={revealed?.title}
        onClose={() => setRevealed(null)}
        footer={
          <Button type="primary" onClick={() => setRevealed(null)}>
            {t("common.done")}
          </Button>
        }
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.accountSecurity.apiKeys.revealWarning")}
        />
        <Typography.Paragraph copyable style={{ wordBreak: "break-all" }}>
          {revealed?.value}
        </Typography.Paragraph>
      </Drawer>
    </Card>
  );
}
