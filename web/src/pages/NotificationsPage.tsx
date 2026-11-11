// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// NotificationsPage is the in-app notification inbox: state filter
// (NEW, READ or ALL), per-item mark read and a mark-all-visible action.

import { useEffect, useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  List,
  Space,
  Tag,
  Typography,
} from "antd";
import { CheckOutlined, CheckCircleOutlined } from "@ant-design/icons";
import {
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { useServerTable } from "../hooks/useServerTable";
import { extractError } from "../api/client";
import {
  listNotifications,
  markNotificationsRead,
  type Notification,
} from "../api/notifications";
import { formatDateTime } from "../utils/format";

// severityColor maps severity values to tag colors.
const severityColor: Record<string, string> = {
  CRITICAL: "red",
  MAJOR: "orange",
  MINOR: "gold",
  WARNING: "blue",
};

// NotificationsPage renders the notification inbox.
export default function NotificationsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  // stateOptions lists inbox state filter values; "all" is reached by
  // clearing the select via FilterSearchBar.
  const stateOptions = [
    { value: "NEW", label: t("pages.notifications.unread") },
    { value: "READ", label: t("pages.notifications.read") },
  ];

  const severityLabels: Record<string, string> = {
    CRITICAL: t("status.critical"),
    MAJOR: t("status.major"),
    MINOR: t("status.minor"),
    WARNING: t("status.warning"),
  };

  const [stateFilter, setStateFilter] = useState(
    (searchParams.get("state") ?? "NEW").toUpperCase(),
  );

  const params = useMemo(
    () => ({ state: stateFilter || undefined }),
    [stateFilter],
  );

  const table = useServerTable("notification-inbox", params, listNotifications);

  // Reflect the filter in the URL for deep links and history.
  useEffect(() => {
    const next = new URLSearchParams();
    if (stateFilter) next.set("state", stateFilter);
    setSearchParams(next, { replace: true });
  }, [stateFilter, setSearchParams]);

  // readMutation marks notifications as read and refreshes the inbox.
  const readMutation = useMutation({
    mutationFn: markNotificationsRead,
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["notification-inbox"],
      });
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // unreadIDs are the visible NEW notifications.
  const unreadIDs = table.items
    .filter((item) => item.state === "NEW")
    .map((item) => item.notification_id);

  return (
    <>
      <PageHeader
        title={t("pages.notifications.title")}
        subtitle={t("pages.notifications.subtitle")}
        helpId="notifications"
        extra={
          <Button
            icon={<CheckCircleOutlined />}
            disabled={unreadIDs.length === 0}
            loading={readMutation.isPending}
            onClick={() => readMutation.mutate(unreadIDs)}
          >
            {t("pages.notifications.markAllRead")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "state",
              placeholder: t("pages.notifications.all"),
              value: stateFilter,
              onChange: setStateFilter,
              options: stateOptions,
            },
          ]}
        />
        <List
          itemLayout="vertical"
          loading={table.loading}
          dataSource={table.items as Notification[]}
          pagination={{
            current: table.pagination.current,
            pageSize: table.pagination.pageSize,
            total: table.pagination.total,
            showSizeChanger: true,
            showTotal: (total) => t("common.totalItems", { total }),
            onChange: (current, pageSize) =>
              table.onChange({ current, pageSize }),
          }}
          renderItem={(notification) => (
            <List.Item
              key={notification.notification_id}
              actions={
                notification.state === "NEW"
                  ? [
                      <Button
                        key="read"
                        size="small"
                        type="link"
                        icon={<CheckOutlined />}
                        onClick={() =>
                          readMutation.mutate([notification.notification_id])
                        }
                      >
                        {t("pages.notifications.markRead")}
                      </Button>,
                    ]
                  : undefined
              }
            >
              <Space wrap>
                {notification.severity && (
                  <Tag color={severityColor[notification.severity]}>
                    {severityLabels[notification.severity] ??
                      notification.severity}
                  </Tag>
                )}
                <Typography.Text
                  strong
                  type={notification.state === "NEW" ? undefined : "secondary"}
                >
                  {notification.title}
                </Typography.Text>
              </Space>
              {notification.body && (
                <Typography.Paragraph
                  type="secondary"
                  style={{ whiteSpace: "pre-wrap", margin: "4px 0 0" }}
                >
                  {notification.body}
                </Typography.Paragraph>
              )}
              <Space style={{ marginTop: 4 }}>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {formatDateTime(notification.created_at)}
                </Typography.Text>
                {notification.device_id && (
                  <Link
                    to={`/devices/${notification.device_id}`}
                    style={{ fontSize: 12 }}
                  >
                    {t("pages.notifications.viewDevice")}
                  </Link>
                )}
              </Space>
            </List.Item>
          )}
        />
      </Card>
    </>
  );
}
