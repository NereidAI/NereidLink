// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StatsPage renders the statistics center. The System tab shows the
// infrastructure snapshot (Go runtime, PostgreSQL, NATS, JetStream, MQTT
// broker) and the Business tab shows tenant-wide telemetry, device, alarm
// and command statistics for a chosen time range. Both tabs refresh on a
// 30-second interval that the user can pause.

import { useMemo, useState } from "react";
import {
  Badge,
  Button,
  Card,
  Col,
  Descriptions,
  Row,
  Segmented,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import ReactECharts from "echarts-for-react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import {
  getBusinessStats,
  getSystemStats,
  type BusinessRange,
  type MetricCount,
} from "../api/stats";
import { formatCompact } from "../utils/format";
import { formatDateTime } from "../utils/format";
// refreshIntervalMS is the automatic refresh period.
const refreshIntervalMS = 30_000;

// severityColor maps alarm severity values to chart and tag colors.
const severityColor: Record<string, string> = {
  CRITICAL: "#ff4d4f",
  MAJOR: "#fa8c16",
  MINOR: "#faad14",
  WARNING: "#1677ff",
};

// alarmStateColor maps alarm lifecycle states to distinct colors.
const alarmStateColor: Record<string, string> = {
  ACTIVE: "#ff4d4f",
  ACK: "#fa8c16",
  CLEARED: "#52c41a",
};

// commandStateColor maps command execution states to distinct colors.
const commandStateColor: Record<string, string> = {
  PENDING: "#1677ff",
  QUEUED: "#722ed1",
  DELIVERED: "#13c2c2",
  SUCCEEDED: "#52c41a",
  FAILED: "#ff4d4f",
  TIMEOUT: "#fa8c16",
  CANCELLED: "#8c8c8c",
  EXPIRED: "#a88b5a",
};



// profilePalette is a qualitative color palette for device profiles.
// Since profiles are dynamic, we cycle through this palette.
// Colors are from Ant Design's qualitative palette plus additions.
const profilePalette = [
  "#1677ff", "#52c41a", "#fa8c16", "#722ed1", "#13c2c2",
  "#eb2f96", "#faad14", "#f5222d", "#08979c", "#2f54eb",
];

// formatBytes renders a byte value in a compact binary unit.
function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(value >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

// formatDuration renders a second count as a compact uptime string.
function formatDuration(totalSeconds: number): string {
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  const parts = [];
  if (days) parts.push(`${days}d`);
  if (hours) parts.push(`${hours}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (!days && !hours) parts.push(`${seconds}s`);
  return parts.join(" ");
}

// StatsPage renders the statistics center.
export default function StatsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState("system");
  const [range, setRange] = useState<BusinessRange>("24h");
  const [paused, setPaused] = useState(false);

  const systemQuery = useQuery({
    queryKey: ["stats-system"],
    queryFn: getSystemStats,
    refetchInterval: !paused && tab === "system" ? refreshIntervalMS : false,
  });

  const businessQuery = useQuery({
    queryKey: ["stats-business", range],
    queryFn: () => getBusinessStats(range),
    refetchInterval: !paused && tab === "business" ? refreshIntervalMS : false,
  });

  const lastUpdated = (tab === "system"
    ? systemQuery.data?.generated_at
    : businessQuery.data?.generated_at);

  return (
    <>
      <PageHeader
        title={t("pages.stats.title")}
        subtitle={t("pages.stats.subtitle")}
        helpId="stats"
        extra={
          <Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {paused
                ? t("pages.stats.refreshPaused")
                : lastUpdated
                  ? t("pages.stats.lastUpdated", {
                      time: new Date(lastUpdated).toLocaleTimeString(),
                    })
                  : ""}
            </Typography.Text>
            <Button
              icon={paused ? <PlayCircleOutlined /> : <PauseCircleOutlined />}
              onClick={() => setPaused((value) => !value)}
            />
            <Button
              icon={<ReloadOutlined />}
              onClick={() =>
                tab === "system"
                  ? void systemQuery.refetch()
                  : void businessQuery.refetch()
              }
            />
          </Space>
        }
      />

      <Card>
        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            {
              key: "system",
              label: t("pages.stats.tabs.system"),
              children: <SystemTab data={systemQuery.data} loading={systemQuery.isFetching} />,
            },
            {
              key: "business",
              label: t("pages.stats.tabs.business"),
              children: (
                <BusinessTab
                  range={range}
                  onRangeChange={setRange}
                  loading={businessQuery.isFetching}
                  groups={businessQuery.data}
                />
              ),
            },
          ]}
        />
      </Card>
    </>
  );
}

// SystemTab renders the infrastructure snapshot.
function SystemTab({
  data,
  loading,
}: {
  data?: Awaited<ReturnType<typeof getSystemStats>>;
  loading: boolean;
}) {
  const { t } = useTranslation();

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} lg={12}>
        <Card
          size="small"
          title={t("pages.stats.system.sections.runtime")}
          loading={loading && !data}
        >
          <Descriptions column={2} size="small">
            <Descriptions.Item label={t("pages.stats.system.fields.uptime")}>
              {data ? formatDuration(data.uptime_sec) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.goroutines")}>
              {data?.go.goroutines ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.heapAlloc")}>
              {data ? formatBytes(data.go.heap_alloc_bytes) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.sysMemory")}>
              {data ? formatBytes(data.go.sys_bytes) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.heapObjects")}>
              {data?.go.heap_objects.toLocaleString() ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.nextGC")}>
              {data ? formatBytes(data.go.next_gc_bytes) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.gcCycles")}>
              {data?.go.gc_cycles ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.lastGCPause")}>
              {data ? `${data.go.last_gc_pause_ms.toFixed(3)} ms` : "-"}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      </Col>

      <Col xs={24} lg={12}>
        <Card
          size="small"
          title={t("pages.stats.system.sections.postgres")}
          loading={loading && !data}
        >
          <Descriptions column={2} size="small">
            <Descriptions.Item label={t("pages.stats.system.fields.maxConnections")}>
              {data?.postgres.max_connections ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.activeConnections")}>
              {data?.postgres.active_connections ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.totalConnections")}>
              {data?.postgres.total_connections ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.idleConnections")}>
              {data?.postgres.idle_connections ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.txCommit")}>
              {data?.postgres.tx_commit.toLocaleString() ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.txRollback")}>
              {data?.postgres.tx_rollback.toLocaleString() ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.blockHitRatio")}>
              {data ? `${data.postgres.block_hit_ratio.toFixed(2)}%` : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.deadlocks")}>
              {data?.postgres.deadlocks ?? "-"}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      </Col>

      <Col xs={24} lg={12}>
        <Card
          size="small"
          title={t("pages.stats.system.sections.nats")}
          loading={loading && !data}
        >
          <Descriptions column={2} size="small">
            <Descriptions.Item label={t("pages.stats.system.fields.inMsgs")}>
              {data?.nats.in_msgs.toLocaleString() ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.outMsgs")}>
              {data?.nats.out_msgs.toLocaleString() ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.inBytes")}>
              {data ? formatBytes(data.nats.in_bytes) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.outBytes")}>
              {data ? formatBytes(data.nats.out_bytes) : "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.reconnects")}>
              {data?.nats.reconnects ?? "-"}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      </Col>

      <Col xs={24} lg={12}>
        <Card
          size="small"
          title={t("pages.stats.system.sections.broker")}
          loading={loading && !data}
        >
          <Descriptions column={2} size="small">
            <Descriptions.Item label={t("pages.stats.system.fields.brokerType")}>
              {data?.broker.type ?? "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.stats.system.fields.onlineClients")}>
              {data?.broker.online_clients ?? "-"}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      </Col>

      <Col span={24}>
        <Card size="small" title={t("pages.stats.system.sections.streams")}>
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            {(data?.streams ?? []).map((stream) => (
              <StreamCard key={stream.name} name={stream.name} stream={stream} />
            ))}
          </Space>
        </Card>
      </Col>
    </Row>
  );
}

// StreamCard renders one JetStream stream and its consumer backlog.
function StreamCard({
  name,
  stream,
}: {
  name: string;
  stream: Awaited<ReturnType<typeof getSystemStats>>["streams"][number];
}) {
  const { t } = useTranslation();

  const columns = [
    { title: t("common.name"), dataIndex: "name" },
    {
      title: t("pages.stats.system.fields.pending"),
      dataIndex: "pending",
      width: 110,
      render: (pending: number) =>
        pending > 0 ? <Tag color="red">{pending}</Tag> : <Tag>0</Tag>,
    },
    {
      title: t("pages.stats.system.fields.redelivered"),
      dataIndex: "redelivered",
      width: 120,
    },
    {
      title: t("pages.stats.system.fields.ackFloor"),
      dataIndex: "ack_floor_seq",
      width: 120,
    },
  ];

  return (
    <Card
      size="small"
      type="inner"
      title={
        <Space>
          <Typography.Text strong>{name}</Typography.Text>
          <Tag color="blue">
            {stream.messages.toLocaleString()} {t("pages.stats.system.fields.messages")}
          </Tag>
          <Tag>{formatBytes(stream.bytes)}</Tag>
          <Typography.Text type="secondary">
            {stream.first_seq} - {stream.last_seq}
          </Typography.Text>
        </Space>
      }
    >
      <Table
        rowKey="name"
        size="small"
        columns={columns}
        dataSource={stream.consumers}
        pagination={false}
      />
    </Card>
  );
}

// BusinessTab renders the tenant business report.
function BusinessTab({
  range,
  onRangeChange,
  loading,
  groups,
}: {
  range: BusinessRange;
  onRangeChange: (value: BusinessRange) => void;
  loading: boolean;
  groups?: Awaited<ReturnType<typeof getBusinessStats>>;
}) {
  const { t } = useTranslation();

  const rangeOptions = useMemo(
    () =>
      (["1h", "24h", "7d"] as const).map((value) => ({
        value,
        label: t(`pages.stats.business.ranges.${value}`),
      })),
    [t],
  );

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Segmented options={rangeOptions} value={range} onChange={(value) => onRangeChange(value as BusinessRange)} />

      <Row gutter={[16, 16]}>
        <Col xs={12} md={6}>
          <Card size="small" loading={loading && !groups}>
            <Statistic
              title={t("pages.stats.business.fields.totalDevices")}
              value={groups?.devices.total ?? 0}
            />
            <Space style={{ marginTop: 8 }}>
              <Badge status="success" text={`${t("pages.stats.business.fields.online")} ${groups?.devices.online ?? 0}`} />
              <Badge status="default" text={`${t("pages.stats.business.fields.offline")} ${groups?.devices.offline ?? 0}`} />
            </Space>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small" loading={loading && !groups}>
            <Statistic
              title={t("pages.stats.business.fields.alarmsCreated")}
              value={groups?.alarms.created ?? 0}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("pages.stats.business.fields.liveAlarms")}: {groups?.alarms.live ?? 0}
            </Typography.Text>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small" loading={loading && !groups}>
            <Statistic
              title={t("pages.stats.business.fields.commandsCreated")}
              value={groups?.commands.created ?? 0}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small" loading={loading && !groups}>
            <Statistic
              title={t("pages.stats.business.fields.activeRuleChains")}
              value={groups?.active_rule_chains ?? 0}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]}>
        <Col xs={24} xl={16}>
          <Card size="small" title={t("pages.stats.business.sections.telemetry")}>
            <TelemetryChart series={groups?.telemetry.series ?? []} />
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card size="small" title={t("pages.stats.business.fields.byProfile")}>
            <DistributionChart
              data={groups?.devices.by_profile ?? []}
              color={profilePalette}
            />
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card size="small" title={t("pages.stats.business.fields.bySeverity")}>
            <DistributionChart
              data={groups?.alarms.by_severity ?? []}
              color={severityColor}
              translateLabels
            />
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card size="small" title={t("pages.stats.business.fields.byState")}>
            <DistributionChart
              data={groups?.alarms.by_state ?? []}
              color={alarmStateColor}
              translateLabels
            />
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card size="small" title={t("pages.stats.business.fields.commandsByState")}>
            <DistributionChart
              data={groups?.commands.by_state ?? []}
              color={commandStateColor}
              translateLabels
            />
          </Card>
        </Col>
      </Row>
    </Space>
  );
}

// TelemetryChart renders the bucketed telemetry volume line chart.
function TelemetryChart({
  series,
}: {
  series: { time: string; count: number }[];
}) {
  const option = useMemo(
    () => ({
      tooltip: {
        trigger: "axis",
        valueFormatter: (value: number | string) =>
          formatCompact(Number(value)),
      },
      grid: { left: 48, right: 16, top: 24, bottom: 32 },
      xAxis: {
        type: "category",
        data: series.map((point) => formatDateTime(point.time)),
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
          itemStyle: { color: "#1677ff" },
          data: series.map((point) => point.count),
        },
      ],
    }),
    [series],
  );

  return (
    <ReactECharts
      option={option}
      style={{ height: 260 }}
      notMerge
    />
  );
}

// statusLabelKey maps machine state/severity values to their translation
// keys; unknown values render verbatim.
const statusLabelKey: Record<string, string> = {
  ACTIVE: "status.active",
  ACK: "status.ack",
  CLEARED: "status.cleared",
  CRITICAL: "status.critical",
  MAJOR: "status.major",
  MINOR: "status.minor",
  WARNING: "status.warning",
  PENDING: "status.pending",
  CLAIMED: "status.claimed",
  QUEUED: "status.queued",
  DELIVERED: "status.delivered",
  SUCCEEDED: "status.succeeded",
  FAILED: "status.failed",
  TIMEOUT: "status.timeout",
  NOT_CONNECTED: "status.notConnected",
  EXPIRED: "status.expired",
  CANCELLED: "status.cancelled",
  RUNNING: "status.running",
  COMPLETED: "status.completed",
};

// DistributionChart renders a label/count distribution as a pie chart.
// When color is a record, labels look up per-label colors. When color is
// an array, it cycles through the palette for dynamic labels (e.g. profiles).
// When translateLabels is set, labels map through the status translation keys.
function DistributionChart({
  data,
  color,
  translateLabels = false,
}: {
  data: MetricCount[];
  color: string | Record<string, string> | string[];
  translateLabels?: boolean;
}) {
  const { t } = useTranslation();

const option = useMemo(() => {
    const resolveLabel = (label: string) =>
      translateLabels && statusLabelKey[label]
        ? t(statusLabelKey[label])
        : label;
    const resolveColor = (label: string, idx: number): string => {
      if (Array.isArray(color)) {
        return color[idx % color.length];
      }
      if (typeof color === "object") {
        return color[label] ?? "#8c8c8c";
      }
      return color;
    };
    return {
      tooltip: { trigger: "item" },
      legend: { bottom: 0, type: "scroll" },
      series: [
        {
          type: "pie",
          radius: ["40%", "68%"],
          center: ["50%", "42%"],
          label: { formatter: "{b}: {c}" },
          data: data.map((metric, idx) => ({
            name: resolveLabel(metric.label),
            value: metric.count,
            itemStyle: { color: resolveColor(metric.label, idx) },
          })),
        },
      ],
    };
  }, [data, color, translateLabels, t]);

  return <ReactECharts option={option} style={{ height: 260 }} notMerge />;
}
