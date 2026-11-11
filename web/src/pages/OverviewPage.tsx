// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OverviewPage renders the tenant operational dashboard: KPI cards,
// telemetry throughput, alarm severity, recent alarms, presence and
// notifications. Initial values come from one REST snapshot; subsequent
// changes are applied from wildcard WebSocket pushes without polling.

import { useEffect, useMemo, useRef, useState } from "react";
import ReactECharts from "echarts-for-react";
import {
  Responsive as ResponsiveGrid,
  WidthProvider,
  type Layout,
} from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  App as AntApp,
  Badge,
  List,
  Space,
  Tag,
  Typography,
} from "antd";
import {
  AlertOutlined,
  ApiOutlined,
  BellOutlined,
  ClockCircleOutlined,
  DashboardOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import { formatCompact } from "../utils/format";

import DashboardPanel from "../components/overview/DashboardPanel";
import StatCard from "../components/overview/StatCard";
import PageHeader from "../components/PageHeader";
import {
  getOverview,
  type MetricCount,
  type OverviewSummary,
  type RecentAlarm,
  type RecentNotification,
} from "../api/overview";
import { useWSStore } from "../store/wsStore";
import { formatDateTime } from "../utils/format";

// ResponsiveGridLayout gains the measured container width.
const ResponsiveGridLayout = WidthProvider(ResponsiveGrid);

// severityColor maps alarm severity values to chart and tag colors.
const severityColor: Record<string, string> = {
  CRITICAL: "#ff4d4f",
  MAJOR: "#fa8c16",
  MINOR: "#faad14",
  WARNING: "#1677ff",
};

// gridLayout declares the initial widget placement and minimum sizes.
const gridLayout: Layout[] = [
  { i: "kpis", x: 0, y: 0, w: 12, h: 2, minH: 2 },
  { i: "telemetry", x: 0, y: 2, w: 8, h: 7, minW: 4, minH: 5 },
  { i: "severity", x: 8, y: 2, w: 4, h: 7, minW: 3, minH: 5 },
  { i: "alarms", x: 0, y: 9, w: 4, h: 6, minW: 3, minH: 4 },
  { i: "presence", x: 4, y: 9, w: 4, h: 6, minW: 3, minH: 4 },
  { i: "notifications", x: 8, y: 9, w: 4, h: 6, minW: 3, minH: 4 },
];

// fiveMinuteBucket returns the RFC3339 start of the 5-minute bucket.
function fiveMinuteBucket(time: string): string {
  const bucketMS = Math.floor(new Date(time).getTime() / 300_000) * 300_000;
  return new Date(bucketMS).toISOString();
}

// shortID trims UUIDs for compact feed rows.
function shortID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// OverviewPage renders the operational dashboard.
export default function OverviewPage() {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [mountedAt] = useState(() => new Date().toISOString());

  // Memoized on t so the severity-label map keeps a stable identity across
  // re-renders and only rebuilds when the language changes.
  const severityText = useMemo<Record<string, string>>(
    () => ({
      CRITICAL: t("status.critical"),
      MAJOR: t("status.major"),
      MINOR: t("status.minor"),
      WARNING: t("status.warning"),
    }),
    [t],
  );

  const alarmStateText: Record<string, string> = {
    ACTIVE: t("status.active"),
    ACK: t("status.ack"),
    CLEARED: t("status.cleared"),
  };

  const livePoints = useWSStore((state) => state.points);
  const livePresence = useWSStore((state) => state.presence);
  const liveAlarms = useWSStore((state) => state.alarms);
  const liveNotifications = useWSStore((state) => state.notifications);
  const wsConnected = useWSStore((state) => state.connected);

  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: ["overview"],
    queryFn: getOverview,
    refetchOnWindowFocus: true,
  });
  const snapshot = query.data;

  useEffect(() => {
    subscribe("overview-telemetry", "telemetry", []);
    subscribe("overview-presence", "presence", []);
    subscribe("overview-alarms", "alarm", []);
    subscribe("overview-notifications", "notification", []);
    return () => {
      unsubscribe("overview-telemetry");
      unsubscribe("overview-presence");
      unsubscribe("overview-alarms");
      unsubscribe("overview-notifications");
    };
  }, [subscribe, unsubscribe]);

  useEffect(() => {
    if (query.error) {
      message.error(t("pages.overview.loadFailed"));
    }
  }, [query.error, message, t]);

  // After a real disconnect/reconnect the REST baseline is stale; refetch it
  // so baseline and re-seeded presence converge without double counting.
  const sawDisconnect = useRef(false);
  useEffect(() => {
    if (!wsConnected) {
      sawDisconnect.current = true;
    } else if (sawDisconnect.current) {
      sawDisconnect.current = false;
      void queryClient.invalidateQueries({ queryKey: ["overview"] });
    }
  }, [wsConnected, queryClient]);

  const model = useMemo(
    () =>
      deriveModel(
        snapshot,
        mountedAt,
        livePoints,
        livePresence,
        liveAlarms,
        liveNotifications,
      ),
    [
      snapshot,
      mountedAt,
      livePoints,
      livePresence,
      liveAlarms,
      liveNotifications,
    ],
  );

  const telemetryOption = useMemo(
    () => ({
      tooltip: {
        trigger: "axis",
        valueFormatter: (value: number | string) =>
          formatCompact(Number(value)),
      },
      grid: { left: 48, right: 20, top: 24, bottom: 36 },
      xAxis: {
        type: "category",
        data: model.telemetryBuckets.map((bucket) =>
          new Date(bucket.label).toLocaleTimeString(),
        ),
      },
      yAxis: {
        type: "value",
        minInterval: 1,
        axisLabel: { formatter: (value: number) => formatCompact(value) },
      },
      series: [
        {
          type: "line",
          smooth: true,
          showSymbol: false,
          areaStyle: { opacity: 0.16 },
          lineStyle: { width: 2, color: "#1677ff" },
          itemStyle: { color: "#1677ff" },
          data: model.telemetryBuckets.map((bucket) => bucket.count),
        },
      ],
    }),
    [model.telemetryBuckets],
  );

  const alarmOption = useMemo(
    () => ({
      tooltip: { trigger: "item" },
      legend: { bottom: 0, type: "scroll" },
      series: [
        {
          type: "pie",
          radius: ["45%", "70%"],
          center: ["50%", "42%"],
          label: { formatter: "{b}: {c}" },
          data: model.alarmSeverities.map((metric) => ({
            name: severityText[metric.label] ?? metric.label,
            value: metric.count,
            itemStyle: { color: severityColor[metric.label] ?? "#8c8c8c" },
          })),
        },
      ],
    }),
    [model.alarmSeverities, severityText],
  );

  return (
    <>
      <PageHeader
        title={t("pages.overview.title")}
        subtitle={t("pages.overview.subtitle")}
        helpId="overview"
      />
      <ResponsiveGridLayout
        className="overview-grid"
        layouts={{
          lg: gridLayout,
          md: gridLayout,
          sm: gridLayout,
          xs: gridLayout,
        }}
        breakpoints={{ lg: 1280, md: 992, sm: 768, xs: 480 }}
        cols={{ lg: 12, md: 12, sm: 1, xs: 1 }}
        rowHeight={42}
        margin={[12, 12]}
        draggableHandle=".ant-card-head"
      >
        <div key="kpis">
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(170px, 1fr))",
              gap: 12,
              height: "100%",
            }}
          >
            <StatCard
              title={t("pages.overview.kpis.devices")}
              value={model.deviceTotal}
              icon={<DashboardOutlined />}
              tone="#1677ff"
              to="/devices"
              helper={t("pages.overview.helpers.onlineOffline", {
                online: model.onlineDevices,
                offline: model.offlineDevices,
              })}
            />
            <StatCard
              title={t("pages.overview.kpis.liveAlarms")}
              value={model.liveAlarms}
              icon={<AlertOutlined />}
              tone="#fa541c"
              to="/alarms?state=LIVE"
            />
            <StatCard
              title={t("pages.overview.kpis.pendingCommands")}
              value={model.pendingCommands}
              icon={<ApiOutlined />}
              tone="#722ed1"
              to="/commands?state=INFLIGHT"
            />
            <StatCard
              title={t("pages.overview.kpis.unread")}
              value={model.unreadNotifications}
              icon={<BellOutlined />}
              tone="#faad14"
              to="/notifications?state=NEW"
            />
            <StatCard
              title={t("pages.overview.kpis.telemetry")}
              value={formatCompact(model.telemetryLastHour)}
              icon={<ClockCircleOutlined />}
              tone="#13c2c2"
              helper={t("pages.overview.helpers.in24h", {
                count: formatCompact(model.telemetryLast24h),
              })}
            />
          </div>
        </div>

        <div key="telemetry">
          <DashboardPanel title={t("pages.overview.panels.throughput")}>
            <ReactECharts
              option={telemetryOption}
              style={{ height: "100%", minHeight: 220 }}
              notMerge
            />
          </DashboardPanel>
        </div>

        <div key="severity">
          <DashboardPanel title={t("pages.overview.panels.bySeverity")}>
            <ReactECharts
              option={alarmOption}
              style={{ height: "100%", minHeight: 220 }}
              notMerge
            />
          </DashboardPanel>
        </div>

        <div key="alarms">
          <DashboardPanel title={t("pages.overview.panels.recentAlarms")}>
            <List
              size="small"
              dataSource={model.recentAlarms}
              renderItem={(alarm) => (
                <List.Item>
                  <Space direction="vertical" size={0} style={{ width: "100%" }}>
                    <Space>
                      <Tag color={severityColor[alarm.severity]}>
                        {severityText[alarm.severity] ?? alarm.severity}
                      </Tag>
                      <Typography.Text strong>{alarm.type}</Typography.Text>
                    </Space>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {shortID(alarm.device_id)} ·{" "}
                      {alarmStateText[alarm.state] ?? alarm.state} ·{" "}
                      {formatDateTime(alarm.started_at)}
                    </Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
          </DashboardPanel>
        </div>

        <div key="presence">
          <DashboardPanel title={t("pages.overview.panels.recentPresence")}>
            <List
              size="small"
              dataSource={model.recentPresence}
              renderItem={(event) => (
                <List.Item>
                  <Space style={{ width: "100%", justifyContent: "space-between" }}>
                    <Badge
                      status={event.state === "ONLINE" ? "success" : "error"}
                      text={shortID(event.device_id)}
                    />
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {new Date(event.time).toLocaleTimeString()}
                    </Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
          </DashboardPanel>
        </div>

        <div key="notifications">
          <DashboardPanel title={t("pages.overview.panels.recentNotifications")}>
            <List
              size="small"
              dataSource={model.recentNotifications}
              renderItem={(notification) => (
                <List.Item>
                  <Space direction="vertical" size={0} style={{ width: "100%" }}>
                    <Space>
                      {notification.severity && (
                        <Tag color={severityColor[notification.severity]}>
                          {severityText[notification.severity] ??
                            notification.severity}
                        </Tag>
                      )}
                      <Typography.Text strong>{notification.title}</Typography.Text>
                    </Space>
                    {notification.body && (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {notification.body}
                      </Typography.Text>
                    )}
                  </Space>
                </List.Item>
              )}
            />
          </DashboardPanel>
        </div>
      </ResponsiveGridLayout>
    </>
  );
}

// dashboardModel is the fully derived view state after applying WS events.
interface dashboardModel {
  deviceTotal: number;
  onlineDevices: number;
  offlineDevices: number;
  liveAlarms: number;
  pendingCommands: number;
  unreadNotifications: number;
  telemetryLastHour: number;
  telemetryLast24h: number;
  telemetryBuckets: MetricCount[];
  alarmSeverities: MetricCount[];
  recentAlarms: RecentAlarm[];
  recentPresence: { time: string; device_id: string; state: string }[];
  recentNotifications: RecentNotification[];
}

// emptyModel is used before the REST snapshot arrives.
function emptyModel(): dashboardModel {
  return {
    deviceTotal: 0,
    onlineDevices: 0,
    offlineDevices: 0,
    liveAlarms: 0,
    pendingCommands: 0,
    unreadNotifications: 0,
    telemetryLastHour: 0,
    telemetryLast24h: 0,
    telemetryBuckets: [],
    alarmSeverities: [],
    recentAlarms: [],
    recentPresence: [],
    recentNotifications: [],
  };
}

// deriveModel applies buffered WS events to the REST snapshot.
function deriveModel(
  snapshot: OverviewSummary | undefined,
  mountedAt: string,
  points: ReturnType<typeof useWSStore.getState>["points"],
  presenceEvents: ReturnType<typeof useWSStore.getState>["presence"],
  alarmEvents: ReturnType<typeof useWSStore.getState>["alarms"],
  notifications: ReturnType<typeof useWSStore.getState>["notifications"],
): dashboardModel {
  if (!snapshot) return emptyModel();

  const model = emptyModel();
  model.deviceTotal = snapshot.device_total;
  model.pendingCommands = snapshot.pending_commands;
  let telemetryLast24h = snapshot.telemetry_last_24h;

  const telemetryBuckets = new Map<string, number>(
    snapshot.telemetry_buckets.map((bucket) => [bucket.label, bucket.count]),
  );
  const severities = new Map<string, number>(
    snapshot.alarm_severities.map((metric) => [metric.label, metric.count]),
  );

  let onlineDevices = snapshot.online_devices;
  let liveAlarms = snapshot.live_alarms;
  let unreadNotifications = snapshot.unread_notifications;
  let telemetryLastHour = snapshot.telemetry_last_hour;

  const now = Date.now();
  for (const point of points) {
    if (point.time < mountedAt) continue;
    const age = now - new Date(point.time).getTime();
    if (age <= 3_600_000) {
      telemetryLastHour++;
      const bucket = fiveMinuteBucket(point.time);
      telemetryBuckets.set(bucket, (telemetryBuckets.get(bucket) ?? 0) + 1);
    }
    if (age <= 86_400_000) {
      telemetryLast24h++;
    }
  }

  // The REST baseline already counts every online device at generated_at;
  // SNAPSHOT placeholders seed that same set and must never be added on top.
  // Only real boundaries strictly newer than the snapshot watermark move
  // the count, so events in the REST/WS race window are not double-counted.
  const presenceState = new Map<string, boolean>();
  for (const event of presenceEvents) {
    if (event.reason === "SNAPSHOT") continue;
    if (event.time <= snapshot.generated_at) continue;
    const wasOnline = presenceState.get(event.device_id);
    const isOnline = event.state === "ONLINE";
    if (wasOnline === undefined) {
      if (isOnline) onlineDevices++;
      else onlineDevices = Math.max(onlineDevices - 1, 0);
    } else if (wasOnline !== isOnline) {
      onlineDevices += isOnline ? 1 : -1;
    }
    presenceState.set(event.device_id, isOnline);
  }
  onlineDevices = Math.min(Math.max(onlineDevices, 0), model.deviceTotal);

  const alarmState = new Map<string, { live: boolean; severity: string }>();
  const recentAlarms = [...snapshot.recent_alarms];
  for (const event of alarmEvents) {
    if (event.started_at < mountedAt) continue;
    const isLive = event.state !== "CLEARED";
    const known = alarmState.get(event.alarm_id);

    if (!known) {
      if (isLive) {
        liveAlarms++;
        severities.set(
          event.severity,
          (severities.get(event.severity) ?? 0) + 1,
        );
        recentAlarms.unshift({
          id: event.alarm_id,
          device_id: event.device_id,
          type: event.alarm_type,
          severity: event.severity,
          state: event.state,
          started_at: event.started_at,
        });
      } else {
        liveAlarms = Math.max(liveAlarms - 1, 0);
        severities.set(
          event.severity,
          Math.max((severities.get(event.severity) ?? 1) - 1, 0),
        );
      }
    } else if (known.live !== isLive) {
      liveAlarms += isLive ? 1 : -1;
      severities.set(
        event.severity,
        (severities.get(event.severity) ?? 0) + (isLive ? 1 : -1),
      );
      if (!isLive) {
        const index = recentAlarms.findIndex(
          (alarm) => alarm.id === event.alarm_id,
        );
        if (index >= 0) recentAlarms.splice(index, 1);
      }
    } else if (isLive && known.severity !== event.severity) {
      severities.set(
        known.severity,
        Math.max((severities.get(known.severity) ?? 1) - 1, 0),
      );
      severities.set(
        event.severity,
        (severities.get(event.severity) ?? 0) + 1,
      );
    }

    alarmState.set(event.alarm_id, {
      live: isLive,
      severity: event.severity,
    });
  }

  const notificationState = new Map<string, boolean>();
  const recentNotifications = [...snapshot.recent_notifications];
  for (const event of notifications) {
    if (event.created_at < mountedAt) continue;
    const wasUnread = notificationState.get(event.notification_id);
    const isUnread = !event.read;
    if (wasUnread === undefined) {
      if (isUnread) unreadNotifications++;
    } else if (wasUnread !== isUnread) {
      unreadNotifications += isUnread ? 1 : -1;
    }
    notificationState.set(event.notification_id, isUnread);

    const row: RecentNotification = {
      id: event.notification_id,
      device_id: "",
      title: event.title,
      body: event.body ?? "",
      severity: event.severity ?? "",
      state: isUnread ? "NEW" : "READ",
      created_at: event.created_at,
    };
    const index = recentNotifications.findIndex(
      (notification) => notification.id === event.notification_id,
    );
    if (index >= 0) recentNotifications[index] = row;
    else recentNotifications.unshift(row);
  }

  model.onlineDevices = onlineDevices;
  model.offlineDevices = model.deviceTotal - onlineDevices;
  model.liveAlarms = Math.max(liveAlarms, 0);
  model.unreadNotifications = Math.max(unreadNotifications, 0);
  model.telemetryLastHour = telemetryLastHour;
  model.telemetryLast24h = telemetryLast24h;
  model.telemetryBuckets = [...telemetryBuckets.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .slice(-12)
    .map(([label, count]) => ({ label, count }));
  model.alarmSeverities = [...severities.entries()]
    .filter(([, count]) => count > 0)
    .map(([label, count]) => ({ label, count }));
  model.recentAlarms = recentAlarms.slice(0, 6);
  model.recentNotifications = recentNotifications.slice(0, 6);
  model.recentPresence = [...presenceEvents]
    .filter(
      (event) =>
        event.reason !== "SNAPSHOT" && event.time >= mountedAt,
    )
    .reverse()
    .slice(0, 8)
    .map((event) => ({
      time: event.time,
      device_id: event.device_id,
      state: event.state,
    }));

  return model;
}
