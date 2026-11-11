// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// AlarmsPage is the alarm center: paged table with state (including the
// compound LIVE value) and severity filters, one-click acknowledgment for
// ACTIVE alarms, and navigation to the originating device.

import { useEffect, useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Divider,
  Drawer,
  Input,
  List,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { CheckOutlined, UnorderedListOutlined } from "@ant-design/icons";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { useServerTable } from "../hooks/useServerTable";
import {
  ackAlarm,
  addAlarmComment,
  assignAlarm,
  listAlarmComments,
  listAlarms,
} from "../api/alarms";
import { listMembers } from "../api/system";
import { currentTenantId } from "../api/tenant";
import { listDevices, type AlarmRow } from "../api/devices";
import { extractError } from "../api/client";
import { formatDateTime } from "../utils/format";

// severityColor maps severity values to tag colors.
const severityColor: Record<string, string> = {
  CRITICAL: "red",
  MAJOR: "orange",
  MINOR: "gold",
  WARNING: "blue",
};

// stateColor maps lifecycle states to tag colors.
const stateColor: Record<string, string> = {
  ACTIVE: "error",
  ACK: "warning",
  CLEARED: "default",
};

// shortID trims UUIDs for compact table cells.
function shortID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// detailsPreStyle sizes the read-only details block to fit the drawer.
const detailsPreStyle = {
  margin: "8px 0 0",
  padding: 10,
  background: "#fafafa",
  border: "1px solid #f0f0f0",
  borderRadius: 6,
  fontSize: 12,
  whiteSpace: "pre-wrap" as const,
  wordBreak: "break-word" as const,
};

// renderDetails pretty-prints the stored alarm details JSON, or signals that
// no detail document was attached. A non-JSON value is shown verbatim.
function renderDetails(raw?: string): string {
  if (!raw) return "-";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// AlarmsPage renders the alarm center.
export default function AlarmsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [selectedAlarm, setSelectedAlarm] = useState<AlarmRow | null>(null);
  const [commentDraft, setCommentDraft] = useState("");

  // stateOptions lists alarm center state filter values.
  const stateOptions = [
    { value: "LIVE", label: t("pages.alarms.live") },
    { value: "ACTIVE", label: t("status.active") },
    { value: "ACK", label: t("status.ack") },
    { value: "CLEARED", label: t("status.cleared") },
  ];

  // severityOptions lists the severity filter values.
  const severityOptions = [
    { value: "CRITICAL", label: t("status.critical") },
    { value: "MAJOR", label: t("status.major") },
    { value: "MINOR", label: t("status.minor") },
    { value: "WARNING", label: t("status.warning") },
  ];

  const severityLabels: Record<string, string> = {
    CRITICAL: t("status.critical"),
    MAJOR: t("status.major"),
    MINOR: t("status.minor"),
    WARNING: t("status.warning"),
  };

  const stateLabels: Record<string, string> = {
    ACTIVE: t("status.active"),
    ACK: t("status.ack"),
    CLEARED: t("status.cleared"),
  };

  const [stateFilter, setStateFilter] = useState(
    (searchParams.get("state") ?? "LIVE").toUpperCase(),
  );
  const [severityFilter, setSeverityFilter] = useState(
    searchParams.get("severity") ?? "",
  );
  const [deviceFilter, setDeviceFilter] = useState(
    searchParams.get("device_id") ?? "",
  );
  const [typeFilter, setTypeFilter] = useState(
    searchParams.get("alarm_type") ?? "",
  );
  const [assigneeFilter, setAssigneeFilter] = useState(
    searchParams.get("assignee") ?? "",
  );

  const params = useMemo(
    () => ({
      state: stateFilter || undefined,
      severity: severityFilter || undefined,
      device_id: deviceFilter || undefined,
      alarm_type: typeFilter || undefined,
      assignee: assigneeFilter || undefined,
    }),
    [stateFilter, severityFilter, deviceFilter, typeFilter, assigneeFilter],
  );

  const table = useServerTable("alarm-center", params, listAlarms);

  // Reflect filters in the URL for deep links and history.
  useEffect(() => {
    const next = new URLSearchParams();
    next.set("state", stateFilter);
    if (severityFilter) next.set("severity", severityFilter);
    if (deviceFilter) next.set("device_id", deviceFilter);
    if (typeFilter) next.set("alarm_type", typeFilter);
    if (assigneeFilter) next.set("assignee", assigneeFilter);
    setSearchParams(next, { replace: true });
  }, [stateFilter, severityFilter, deviceFilter, typeFilter,
    assigneeFilter, setSearchParams]);

  // Lookup data for the filter dropdowns: tenant members back both the
  // assignee filter and the drawer assignment; devices back the device
  // filter. Both load once.
  const membersQuery = useQuery({
    queryKey: ["alarm-members", currentTenantId()],
    queryFn: () => listMembers(currentTenantId()),
  });

  const devicesQuery = useQuery({
    queryKey: ["alarm-filter-devices", currentTenantId()],
    queryFn: async () => {
      const page = await listDevices({
        page: 1,
        page_size: 1000,
      });
      return page.items;
    },
  });

  // ackMutation acknowledges one ACTIVE alarm and refreshes the page.
  const ackMutation = useMutation({
    mutationFn: ackAlarm,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["alarm-center"] });
      message.success(t("pages.alarms.acknowledged"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // Distinct alarm types are gathered from the currently loaded rows. A type
  // cannot match without also appearing in the result rows, so this stays
  // consistent with the visible page.
  const typeOptions = useMemo(() => {
    const seen = new Set<string>();
    (table.items as AlarmRow[]).forEach((row) => seen.add(row.alarm_type));
    return [...seen].sort().map((value) => ({ value, label: value }));
  }, [table.items]);

  const deviceFilterOptions = useMemo(
    () =>
      (devicesQuery.data ?? []).map((device) => ({
        value: device.id,
        label: device.alias || device.canonical_name,
      })),
    [devicesQuery.data],
  );

  const memberOptions = useMemo(
    () =>
      (membersQuery.data ?? []).map((member) => ({
        value: member.user_id,
        label: member.display_name || member.email,
      })),
    [membersQuery.data],
  );

  const commentsQuery = useQuery({
    queryKey: ["alarm-comments", selectedAlarm?.alarm_id ?? ""],
    queryFn: () => listAlarmComments(selectedAlarm?.alarm_id ?? ""),
    enabled: selectedAlarm !== null,
  });

  const assignMutation = useMutation({
    mutationFn: (assigneeID: string) =>
      assignAlarm(selectedAlarm?.alarm_id ?? "", assigneeID),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["alarm-center"] });
      message.success(t("pages.alarms.assigned"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const commentMutation = useMutation({
    mutationFn: (body: string) =>
      addAlarmComment(selectedAlarm?.alarm_id ?? "", body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["alarm-comments", selectedAlarm?.alarm_id],
      });
      setCommentDraft("");
      message.success(t("pages.alarms.commentAdded"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("common.severity"),
      dataIndex: "severity",
      width: 110,
      render: (severity: string) => (
        <Tag color={severityColor[severity]}>
          {severityLabels[severity] ?? severity}
        </Tag>
      ),
    },
    { title: t("common.type"), dataIndex: "alarm_type" },
    {
      title: t("common.device"),
      dataIndex: "device_id",
      render: (id: string, record: AlarmRow) => (
        <Link to={`/devices/${id}`}>{record.device_name || shortID(id)}</Link>
      ),
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      width: 110,
      render: (state: string) => (
        <Tag color={stateColor[state]}>
          {stateLabels[state] ?? state}
        </Tag>
      ),
    },
    {
      title: t("pages.alarms.assignee"),
      dataIndex: "assignee_name",
      width: 150,
      render: (value: string) => value ?? t("pages.alarms.unassigned"),
    },
    {
      title: t("common.started"),
      dataIndex: "started_at",
      width: 220,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 170,
      render: (_: unknown, record: AlarmRow) => (
        <Space size={4}>
          {record.state === "ACTIVE" ? (
            <Button
              size="small"
              icon={<CheckOutlined />}
              loading={ackMutation.isPending}
              onClick={(event) => {
                event.stopPropagation();
                ackMutation.mutate(record.alarm_id);
              }}
            >
              {t("pages.alarms.ack")}
            </Button>
          ) : null}
          <Button
            size="small"
            icon={<UnorderedListOutlined />}
            onClick={(event) => {
              event.stopPropagation();
              setSelectedAlarm(record);
            }}
          >
            {t("pages.alarms.details")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.alarms.title")}
        subtitle={t("pages.alarms.subtitle")}
        helpId="alarms"
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "state",
              placeholder: t("common.state"),
              value: stateFilter,
              onChange: setStateFilter,
              options: stateOptions,
            },
            {
              key: "severity",
              placeholder: t("pages.alarms.allSeverities"),
              value: severityFilter,
              onChange: setSeverityFilter,
              options: severityOptions,
            },
            {
              key: "type",
              placeholder: t("common.type"),
              value: typeFilter,
              onChange: setTypeFilter,
              options: typeOptions,
            },
            {
              key: "device",
              placeholder: t("common.device"),
              value: deviceFilter,
              onChange: setDeviceFilter,
              options: deviceFilterOptions,
            },
            {
              key: "assignee",
              placeholder: t("pages.alarms.assignee"),
              value: assigneeFilter,
              onChange: setAssigneeFilter,
              options: [
                {
                  value: "none",
                  label: t("pages.alarms.unassigned"),
                },
                ...memberOptions,
              ],
            },
          ]}
        />
        <Table
          rowKey="alarm_id"
          loading={table.loading}
          columns={columns}
          dataSource={table.items as AlarmRow[]}
          pagination={table.pagination}
          onRow={(record) => ({
            onClick: () => navigate(`/devices/${record.device_id}`),
            style: { cursor: "pointer" },
          })}
          onChange={(pagination) =>
            table.onChange({
              current: pagination.current,
              pageSize: pagination.pageSize,
            })
          }
        />
      </Card>
      <Drawer
        title={t("pages.alarms.details")}
        open={selectedAlarm !== null}
        onClose={() => setSelectedAlarm(null)}
        width={520}
        destroyOnHidden
      >
        {selectedAlarm ? (
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Descriptions
              size="small"
              column={1}
              bordered
              labelStyle={{ width: 130 }}
              items={[
                {
                  label: t("common.severity"),
                  children: (
                    <Tag color={severityColor[selectedAlarm.severity]}>
                      {severityLabels[selectedAlarm.severity] ??
                        selectedAlarm.severity}
                    </Tag>
                  ),
                },
                {
                  label: t("common.type"),
                  children: selectedAlarm.alarm_type,
                },
                {
                  label: t("common.state"),
                  children: (
                    <Tag color={stateColor[selectedAlarm.state]}>
                      {stateLabels[selectedAlarm.state] ?? selectedAlarm.state}
                    </Tag>
                  ),
                },
                {
                  label: t("common.device"),
                  children: (
                    <Link to={`/devices/${selectedAlarm.device_id}`}>
                      {selectedAlarm.device_name || shortID(selectedAlarm.device_id)}
                    </Link>
                  ),
                },
                {
                  label: t("pages.alarms.startedAt"),
                  children: formatDateTime(selectedAlarm.started_at),
                },
                {
                  label: t("pages.alarms.ackedAt"),
                  children: selectedAlarm.acked_at
                    ? formatDateTime(selectedAlarm.acked_at)
                    : "-",
                },
                {
                  label: t("pages.alarms.clearedAt"),
                  children: selectedAlarm.cleared_at
                    ? formatDateTime(selectedAlarm.cleared_at)
                    : "-",
                },
              ]}
            />

            <div>
              <Typography.Text strong>{t("pages.alarms.detailsContent")}</Typography.Text>
              <Typography.Paragraph>
                <pre style={detailsPreStyle}>{renderDetails(selectedAlarm.details)}</pre>
              </Typography.Paragraph>
            </div>

            <Divider style={{ margin: "4px 0" }} orientation="left">
              {t("pages.alarms.assignment")}
            </Divider>
            <Select
              style={{ width: "100%" }}
              value={selectedAlarm.assignee_id ?? ""}
              placeholder={t("pages.alarms.unassigned")}
              loading={membersQuery.isLoading}
              allowClear
              options={(membersQuery.data ?? []).map((member) => ({
                value: member.user_id,
                label: member.display_name || member.email,
              }))}
              onChange={(value) => assignMutation.mutate(value ?? "")}
            />

            <Divider style={{ margin: "4px 0" }} orientation="left">
              {t("pages.alarms.comments")}
            </Divider>
            <List
              size="small"
              bordered
              loading={commentsQuery.isLoading}
              dataSource={commentsQuery.data ?? []}
              locale={{ emptyText: t("pages.alarms.noComments") }}
              renderItem={(comment) => (
                <List.Item>
                  <List.Item.Meta
                    title={`${comment.author_name} · ${formatDateTime(comment.created_at)}`}
                    description={comment.body}
                  />
                </List.Item>
              )}
            />
            <Space.Compact style={{ width: "100%" }}>
              <Input
                value={commentDraft}
                placeholder={t("pages.alarms.commentPlaceholder")}
                onChange={(event) => setCommentDraft(event.target.value)}
                onPressEnter={() => {
                  if (commentDraft.trim()) {
                    commentMutation.mutate(commentDraft.trim());
                  }
                }}
              />
              <Button
                type="primary"
                loading={commentMutation.isPending}
                disabled={!commentDraft.trim()}
                onClick={() => commentMutation.mutate(commentDraft.trim())}
              >
                {t("pages.alarms.addComment")}
              </Button>
            </Space.Compact>
          </Space>
        ) : null}
      </Drawer>
    </>
  );
}
