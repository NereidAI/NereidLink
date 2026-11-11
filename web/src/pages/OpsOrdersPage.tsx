// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OpsOrdersPage is the super administrator's confirmation queue for
// ADR-0023. Operators open destructive cleanup orders from the CLI;
// the super administrator reviews the full payload and content hash
// here and confirms or refuses. Orders cannot be created from the web.

import { useState, type ReactNode } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Drawer,
  Input,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";
import {
  listOpsOrders,
  reviewOpsOrder,
  type OpsOrder,
} from "../api/system";

// statusFilters are the queue segments offered in the status selector.
const statusFilters = ["PENDING", "APPROVED", "EXECUTED", "REJECTED", "EXPIRED", "CANCELLED"];

// statusColors keeps order states visually stable.
const statusColors: Record<string, string> = {
  PENDING: "orange",
  APPROVED: "blue",
  EXECUTED: "green",
  REJECTED: "red",
  EXPIRED: "default",
  CANCELLED: "default",
};

// OpsOrdersPage renders the operation order queue.
export default function OpsOrdersPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const user = authStore((state) => state.user);
  const isZh = i18n.language.startsWith("zh");
  const [status, setStatus] = useState("PENDING");
  const [reviewing, setReviewing] = useState<OpsOrder | undefined>();

  // formatTime renders the locale-specific absolute timestamp.
  const formatTime = (value?: string) =>
    value
      ? dayjs(value).format(isZh ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A")
      : "-";

  const ordersQuery = useQuery({
    queryKey: ["ops-orders", status],
    queryFn: () => listOpsOrders(status),
    refetchInterval: 5000,
  });
  const orders = ordersQuery.data ?? [];

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["ops-orders"] });
  const onError = (error: unknown) =>
    message.error(extractError(error).message);

  const reviewMutation = useMutation({
    mutationFn: (args: { decision: "approve" | "reject"; reason: string }) =>
      reviewOpsOrder(reviewing?.id ?? "", args.decision, args.reason),
    onSuccess: async (result) => {
      await invalidate();
      setReviewing(undefined);
      message.success(
        t("pages.opsOrders.toasts.decided", { status: result.status }),
      );
    },
    onError,
  });

  if (!user?.is_platform_admin) {
    return (
      <>
        <PageHeader title={t("pages.opsOrders.title")} />
        <Alert type="error" showIcon message={t("pages.opsOrders.forbidden")} />
      </>
    );
  }

  const columns = [
    {
      title: t("pages.opsOrders.columns.action"),
      dataIndex: "action",
      width: 200,
    },
    {
      title: t("pages.opsOrders.columns.summary"),
      dataIndex: "summary",
    },
    {
      title: t("pages.opsOrders.columns.initiator"),
      key: "initiator",
      width: 220,
      render: (_: unknown, row: OpsOrder) => (
        <Space size={4}>
          <span>{row.initiator_name}</span>
          <Tag>{row.initiator_channel}</Tag>
        </Space>
      ),
    },
    {
      title: t("pages.opsOrders.columns.status"),
      dataIndex: "status",
      width: 130,
      render: (value: string) => (
        <Tag color={statusColors[value]}>
          {t(`pages.opsOrders.states.${value}`, { defaultValue: value })}
        </Tag>
      ),
    },
    {
      title: t("pages.opsOrders.columns.expires"),
      dataIndex: "expires_at",
      width: 190,
      render: (value: string) => formatTime(value),
    },
    {
      title: t("pages.opsOrders.columns.actions"),
      key: "actions",
      width: 120,
      render: (_: unknown, row: OpsOrder) => (
        <Button size="small" onClick={() => setReviewing(row)}>
          {t("pages.opsOrders.actions.view")}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.opsOrders.title")}
        subtitle={t("pages.opsOrders.subtitle")}
        helpId="system-ops-orders"
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "status",
              placeholder: t("pages.opsOrders.columns.status"),
              value: status,
              onChange: setStatus,
              options: statusFilters.map((value) => ({
                value,
                label: t(`pages.opsOrders.states.${value}`),
              })),
            },
          ]}
        />
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.opsOrders.confirmationHint")}
        />
        <Table
          rowKey="id"
          loading={ordersQuery.isFetching}
          columns={columns}
          dataSource={orders}
          pagination={false}
        />
      </Card>

      <ReviewDrawer
        order={reviewing}
        loading={reviewMutation.isPending}
        formatTime={formatTime}
        onClose={() => setReviewing(undefined)}
        onDecide={(decision, reason) =>
          reviewMutation.mutate({ decision, reason })
        }
      />
    </>
  );
}

// ReviewDrawer shows the complete order with payload and hash and the
// confirmation controls enabled while the order is PENDING.
function ReviewDrawer({
  order,
  loading,
  formatTime,
  onClose,
  onDecide,
}: {
  order?: OpsOrder;
  loading: boolean;
  formatTime: (value?: string) => string;
  onClose: () => void;
  onDecide: (decision: "approve" | "reject", reason: string) => void;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const canDecide = order?.status === "PENDING";

  return (
    <Drawer
      open={!!order}
      width={720}
      title={t("pages.opsOrders.reviewTitle")}
      onClose={onClose}
      destroyOnHidden
    >
      {order && (
        <Space direction="vertical" size={16} style={{ width: "100%" }}>
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label={t("pages.opsOrders.fields.action")}>
              {order.action}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.opsOrders.columns.initiator")}>
              {order.initiator_name} ({order.initiator_channel})
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.opsOrders.columns.status")}>
              {t(`pages.opsOrders.states.${order.status}`, {
                defaultValue: order.status,
              })}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.opsOrders.columns.created")}>
              {formatTime(order.created_at)}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.opsOrders.columns.expires")}>
              {formatTime(order.expires_at)}
            </Descriptions.Item>
            {order.reviewer_name && (
              <Descriptions.Item label={t("pages.opsOrders.columns.reviewer")}>
                {order.reviewer_name} ({order.reviewer_channel})
              </Descriptions.Item>
            )}
            <Descriptions.Item label={t("pages.opsOrders.fields.hash")}>
              <Tooltip title={order.content_hash}>
                <code>{order.content_hash.slice(0, 32)}...</code>
              </Tooltip>
            </Descriptions.Item>
          </Descriptions>

          <div>
            <TypographyTitle>{t("pages.opsOrders.fields.payload")}</TypographyTitle>
            <Input.TextArea
              readOnly
              rows={8}
              value={JSON.stringify(order.payload, null, 2)}
              style={{ fontFamily: "monospace" }}
            />
          </div>

          {canDecide ? (
            <div>
              <TypographyTitle>
                {t("pages.opsOrders.fields.decisionReason")}
              </TypographyTitle>
              <Input.TextArea
                rows={2}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
              />
              <Space style={{ marginTop: 12 }}>
                <Popconfirm
                  title={t("pages.opsOrders.confirm.approve")}
                  onConfirm={() => onDecide("approve", reason)}
                >
                  <Button type="primary" loading={loading}>
                    {t("pages.opsOrders.actions.approve")}
                  </Button>
                </Popconfirm>
                <Button
                  danger
                  loading={loading}
                  onClick={() => onDecide("reject", reason)}
                >
                  {t("pages.opsOrders.actions.reject")}
                </Button>
              </Space>
            </div>
          ) : (
            order.exec_result && Object.keys(order.exec_result).length > 0 && (
              <Alert
                type="info"
                showIcon
                message={`${t("pages.opsOrders.fields.execResult")}: ${JSON.stringify(order.exec_result)}`}
              />
            )
          )}
        </Space>
      )}
    </Drawer>
  );
}

// TypographyTitle is a small heading used inside the drawer.
function TypographyTitle({ children }: { children: ReactNode }) {
  return (
    <div style={{ fontWeight: 600, marginBottom: 8 }}>{children}</div>
  );
}
