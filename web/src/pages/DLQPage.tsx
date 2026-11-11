// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DLQPage is the telemetry dead-letter queue: server-paged table with row
// selection and bulk replay/delete actions. REST-only; DLQ rows are
// operational diagnostics and need no push.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Table,
  Tag,
} from "antd";
import {
  DeleteOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { useDebounce } from "../hooks/useDebounce";
import {
  deleteDLQ,
  listDLQ,
  listStorageHooks,
  replayDLQ,
  type DLQRecord,
} from "../api/hooks";
import { formatDateTime } from "../utils/format";

// DLQPage renders the dead-letter queue page.
export default function DLQPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const [selected, setSelected] = useState<number[]>([]);
  const [hookFilter, setHookFilter] = useState("");
  const [deviceDraft, setDeviceDraft] = useState("");
  const deviceFilter = useDebounce(deviceDraft, 300);

  const hooksQuery = useQuery({
    queryKey: ["storage-hooks"],
    queryFn: listStorageHooks,
  });
  const hookOptions = (hooksQuery.data ?? []).map((hook) => ({
    value: hook.name,
    label: hook.name,
  }));

  const query = useQuery({
    queryKey: ["dlq", page, pageSize, hookFilter, deviceFilter],
    queryFn: () =>
      listDLQ(page, pageSize, {
        hookName: hookFilter || undefined,
        device: deviceFilter || undefined,
      }),
  });
  const records = query.data?.items ?? [];

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ["dlq"] });
    setSelected([]);
  };

  const replayMutation = useMutation({
    mutationFn: replayDLQ,
    onSuccess: async (count) => {
      await invalidate();
      message.success(t("pages.dlq.toasts.replayed", { count }));
    },
    onError: () => message.error(t("pages.dlq.toasts.replayFailed")),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteDLQ,
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.dlq.toasts.deleted"));
    },
    onError: () => message.error(t("pages.dlq.toasts.deleteFailed")),
  });

  const columns = useMemo(
    () => [
      {
        title: t("pages.dlq.time"),
        dataIndex: "time",
        width: 200,
        render: (time: string) => formatDateTime(time),
      },
      { title: t("common.device"), dataIndex: "device_id", width: 280 },
      { title: t("common.key"), dataIndex: "measurement_key", width: 130 },
      {
        title: t("pages.dlq.hook"),
        dataIndex: "hook_name",
        width: 150,
        render: (name: string) => <Tag color="orange">{name}</Tag>,
      },
      { title: t("pages.dlq.reason"), dataIndex: "reason", ellipsis: true },
    ],
    [t],
  );

  const hasSelection = selected.length > 0;

  // changeFilter applies a new filter value, returning to page one and
  // dropping the stale cross-page selection.
  const changeFilter = (apply: () => void) => {
    apply();
    setPage(1);
    setSelected([]);
  };

  return (
    <>
      <PageHeader
        title={t("pages.dlq.title")}
        subtitle={t("pages.dlq.subtitle")}
        helpId="dlq"
        extra={
          <Space>
            <Button
              icon={<ReloadOutlined />}
              onClick={() => void query.refetch()}
            >
              {t("common.refresh")}
            </Button>
            <Popconfirm
              title={t("pages.dlq.confirm.replayTitle", {
                count: selected.length,
              })}
              description={t("pages.dlq.confirm.replayDesc")}
              disabled={!hasSelection}
              onConfirm={() => replayMutation.mutate(selected)}
            >
              <Button
                type="primary"
                icon={<PlayCircleOutlined />}
                disabled={!hasSelection}
                loading={replayMutation.isPending}
              >
                {t("pages.dlq.replay")}
              </Button>
            </Popconfirm>
            <Popconfirm
              title={t("pages.dlq.confirm.deleteTitle", {
                count: selected.length,
              })}
              disabled={!hasSelection}
              okButtonProps={{ danger: true }}
              onConfirm={() => deleteMutation.mutate(selected)}
            >
              <Button
                danger
                icon={<DeleteOutlined />}
                disabled={!hasSelection}
                loading={deleteMutation.isPending}
              >
                {t("common.delete")}
              </Button>
            </Popconfirm>
          </Space>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={deviceDraft}
          searchPlaceholder={t("pages.dlq.searchDevice")}
          onSearchChange={(value) =>
            changeFilter(() => setDeviceDraft(value))
          }
          filters={[
            {
              key: "hook",
              value: hookFilter,
              onChange: (value) => changeFilter(() => setHookFilter(value)),
              placeholder: t("pages.dlq.filterHook"),
              options: hookOptions,
            },
          ]}
        />
        <Table
          rowKey="id"
          loading={query.isFetching}
          columns={columns}
          dataSource={records as DLQRecord[]}
          rowSelection={{
            selectedRowKeys: selected,
            onChange: (keys) => setSelected(keys.map(Number)),
          }}
          pagination={{
            current: query.data?.page ?? page,
            pageSize: query.data?.page_size ?? pageSize,
            total: query.data?.total ?? 0,
            showSizeChanger: true,
            showTotal: (total) =>
              t("common.totalItems", { total }),
          }}
          onChange={(next) => {
            setPage(next.current ?? 1);
            setPageSize(next.pageSize ?? 10);
          }}
        />
      </Card>
    </>
  );
}
