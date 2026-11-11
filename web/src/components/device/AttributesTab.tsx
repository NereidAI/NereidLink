// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// AttributesTab lists device shadow attributes and upserts platform-writable
// scopes (SERVER_SCOPE/SHARED_SCOPE). CLIENT scope is read-only here.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Form,
  Input,
  Drawer,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  deleteAttribute,
  listAttributes,
  putAttribute,
  type Attribute,
} from "../../api/devices";
import { extractError } from "../../api/client";
import { useWSStore } from "../../store/wsStore";
import FilterSearchBar from "../../components/FilterSearchBar";
import { formatDateTime } from "../../utils/format";

interface AttributesTabProps {
  // deviceId is the detailed device.
  deviceId: string;
}

// scopeTag colors keep scope identity visually stable.
const scopeColors: Record<string, string> = {
  CLIENT: "blue",
  SERVER_SCOPE: "green",
  SHARED_SCOPE: "orange",
};

interface EditorValues {
  scope: string;
  key: string;
  rawValue: string;
}

// AttributesTab renders the attribute table and editor drawer.
export default function AttributesTab({ deviceId }: AttributesTabProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const scopeLabels: Record<string, string> = {
    CLIENT: t("enums.scopeClient"),
    SERVER_SCOPE: t("enums.scopeServer"),
    SHARED_SCOPE: t("enums.scopeShared"),
  };
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Attribute | undefined>();
  const [form] = Form.useForm<EditorValues>();

  const query = useQuery({
    queryKey: ["device-attributes", deviceId],
    queryFn: () => listAttributes(deviceId),
  });
  // Memoized so downstream useMemo dependencies keep a stable identity
  // instead of seeing a fresh [] fallback on every render.
  const attributes = useMemo(() => query.data ?? [], [query.data]);

  // Live updates: subscribe to this device's attribute changes and patch the
  // React Query cache per key (no polling, server push only). This covers
  // server writes from other sessions and CLIENT-scope device uplinks.
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const attributeEvents = useWSStore((state) => state.attributeEvents);

  // Baseline into the global rolling buffer captured when this tab mounts;
  // only frames appended afterwards are applied.
  const cursorRef = useRef<number | null>(null);

  useEffect(() => {
    cursorRef.current = useWSStore.getState().attributeEvents.length;
    subscribe("device-attributes", "attribute", [deviceId]);
    return () => unsubscribe("device-attributes");
  }, [deviceId, subscribe, unsubscribe]);

  useEffect(() => {
    if (cursorRef.current === null) return;
    const fresh = attributeEvents
      .slice(cursorRef.current)
      .filter((event) => event.device_id === deviceId);
    cursorRef.current = attributeEvents.length;
    if (fresh.length === 0) return;
    queryClient.setQueryData<Attribute[]>(
      ["device-attributes", deviceId],
      (previous) => {
        const rows = previous ? [...previous] : [];
        for (const event of fresh) {
          const index = rows.findIndex(
            (row) => row.scope === event.scope && row.key === event.key,
          );
          // A null value is the deletion marker: drop the row so live
          // operator UIs reflect platform-side removal immediately.
          if (event.value === null) {
            if (index >= 0) rows.splice(index, 1);
            continue;
          }
          const next: Attribute = {
            scope: event.scope,
            key: event.key,
            value: event.value,
            updated_at: event.updated_at,
          };
          if (index >= 0) {
            rows[index] = next;
          } else {
            rows.push(next);
          }
        }
        return rows;
      },
    );
  }, [attributeEvents, deviceId, queryClient]);

  const [keyword, setKeyword] = useState("");
  const [scopeFilter, setScopeFilter] = useState("");

  // scopeFilterOptions reuses the localized scope enum labels also shown
  // in the scope tag.
  const scopeFilterOptions = (
    ["CLIENT", "SERVER_SCOPE", "SHARED_SCOPE"] as const
  ).map((scope) => ({ value: scope, label: scopeLabels[scope] }));

  // Filtering is client-side: the endpoint returns all attributes of one
  // device and the set is small.
  const filteredAttributes = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    return attributes.filter((row) => {
      if (scopeFilter && row.scope !== scopeFilter) return false;
      if (term && !row.key.toLowerCase().includes(term)) return false;
      return true;
    });
  }, [attributes, keyword, scopeFilter]);

  const mutation = useMutation({
    mutationFn: async (values: EditorValues) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(values.rawValue);
      } catch {
        parsed = values.rawValue;
      }
      return putAttribute(deviceId, values.scope, values.key, parsed);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["device-attributes", deviceId],
      });
      message.success(t("pages.deviceDetail.attributes.saved"));
      setModalOpen(false);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: (row: Attribute) =>
      deleteAttribute(deviceId, row.scope, row.key),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["device-attributes", deviceId],
      });
      message.success(t("pages.deviceDetail.attributes.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("common.scope"),
      dataIndex: "scope",
      render: (scope: string) => (
        <Tag color={scopeColors[scope]}>
          {scopeLabels[scope] ?? scope}
        </Tag>
      ),
    },
    { title: t("common.key"), dataIndex: "key" },
    {
      title: t("common.value"),
      dataIndex: "value",
      render: (value: unknown) => (
        <code>{JSON.stringify(value)}</code>
      ),
    },
    {
      title: t("common.updated"),
      dataIndex: "updated_at",
      render: (time: string) => formatDateTime(time),
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 120,
      render: (_: unknown, row: Attribute) => {
        const platformManaged =
          row.scope === "SERVER_SCOPE" || row.scope === "SHARED_SCOPE";
        if (!platformManaged) {
          return (
            <Tooltip title={t("pages.deviceDetail.attributes.clientReadOnly")}>
              <span style={{ color: "rgba(0,0,0,0.45)" }}>
                {t("common.readOnly")}
              </span>
            </Tooltip>
          );
        }
        return (
          <Space size="small">
            <Button
              type="link"
              size="small"
              onClick={() => {
                setEditing(row);
                setModalOpen(true);
              }}
            >
              {t("common.edit")}
            </Button>
            <Popconfirm
              title={t("pages.deviceDetail.attributes.deleteConfirmTitle", {
                key: row.key,
              })}
              description={t(
                "pages.deviceDetail.attributes.deleteConfirmDescription",
              )}
              okText={t("common.delete")}
              okButtonProps={{ danger: true }}
              cancelText={t("common.cancel")}
              onConfirm={() => deleteMutation.mutate(row)}
            >
              <Button
                type="link"
                size="small"
                danger
                icon={<DeleteOutlined />}
              />
            </Popconfirm>
          </Space>
        );
      },
    },
  ];

  return (
    <Card
      extra={
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(undefined);
            setModalOpen(true);
          }}
        >
          {t("pages.deviceDetail.attributes.add")}
        </Button>
      }
    >
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t(
          "pages.deviceDetail.attributes.searchPlaceholder",
        )}
        filters={[
          {
            key: "scope",
            placeholder: t("common.scope"),
            value: scopeFilter,
            onChange: setScopeFilter,
            options: scopeFilterOptions,
          },
        ]}
      />
      <Table
        rowKey={(row) => `${row.scope}:${row.key}`}
        loading={query.isFetching}
        columns={columns}
        dataSource={filteredAttributes}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <Drawer
        open={modalOpen}
        title={
          editing
            ? t("pages.deviceDetail.attributes.edit")
            : t("pages.deviceDetail.attributes.add")
        }
        width={720}
        onClose={() => setModalOpen(false)}
        footer={null}
        destroyOnHidden
        afterOpenChange={(visible) => {
          if (visible && editing) {
            form.setFieldsValue({
              scope: editing.scope,
              key: editing.key,
              rawValue: JSON.stringify(editing.value),
            });
          }
        }}
      >
        <Form
          form={form}
          layout="vertical"
          preserve={false}
          initialValues={{ scope: "SERVER_SCOPE", rawValue: '""' }}
          onFinish={(values) => mutation.mutate(values)}
        >
          <Form.Item
            label={t("common.scope")}
            name="scope"
            rules={[{ required: true }]}
          >
            <Select
              disabled={!!editing}
              options={[
                { value: "SERVER_SCOPE", label: t("enums.scopeServer") },
                { value: "SHARED_SCOPE", label: t("enums.scopeShared") },
              ]}
            />
          </Form.Item>
          <Form.Item
            label={t("common.key")}
            name="key"
            rules={[
              {
                required: true,
                message: t("pages.deviceDetail.attributes.keyRequired"),
              },
            ]}
          >
            <Input
              placeholder={t("pages.deviceDetail.attributes.keyPlaceholder")}
              disabled={!!editing}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.deviceDetail.attributes.valueJson")}
            name="rawValue"
            rules={[
              {
                required: true,
                message: t("pages.deviceDetail.attributes.valueRequired"),
              },
            ]}
          >
            <Input.TextArea rows={4} />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setModalOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="primary" htmlType="submit" loading={mutation.isPending}>
                {t("common.save")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>
    </Card>
  );
}
