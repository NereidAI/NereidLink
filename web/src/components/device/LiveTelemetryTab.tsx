// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// LiveTelemetryTab renders a live chart for one device. Measurement keys
// are discovered dynamically from stored telemetry and extended with keys
// first seen in live frames. Numbers render as a line and booleans as a
// 0/1 step chart; string measurements carry no numeric meaning and are
// excluded from the chart selector (they remain visible in the All
// measurements table). Historical points seed the chart; telemetry WS
// frames append new values. Polling is never used.

import { useEffect, useMemo, useState } from "react";
import { Card, Empty, Select, Space, Tabs, Typography } from "antd";
import ReactECharts from "echarts-for-react";
import { useQuery } from "@tanstack/react-query";

import type {
  TelemetryKey,
  TelemetryRange,
  TelemetrySeriesPoint,
  TelemetryValueType,
} from "../../api/devices";
import { listTelemetryKeys, listTelemetrySeries } from "../../api/devices";
import { useWSStore, type TelemetryPoint } from "../../store/wsStore";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import MeasurementsTable from "./MeasurementsTable";

// MAX_CHART_POINTS bounds the rendered series like the rolling buffers.
const MAX_CHART_POINTS = 300;

// rangePresets lists the historical window choices in header order.
const rangePresets: TelemetryRange[] = [
  "5m",
  "15m",
  "30m",
  "1h",
  "2h",
  "6h",
  "12h",
  "24h",
  "7d",
];

interface LiveTelemetryTabProps {
  // deviceId is the detailed device.
  deviceId: string;
}

// innerViewKey names the two Telemetry sub-tabs.
type innerViewKey = "chart" | "measurements";

// inferValueType classifies a live point whose key is not yet discovered.
function inferValueType(point: TelemetryPoint): TelemetryValueType {
  if (point.value_type === "boolean" || point.value_type === "string") {
    return point.value_type;
  }
  if (typeof point.v_bool === "boolean") {
    return "boolean";
  }
  if (point.v_string) {
    return "string";
  }
  return "number";
}

// LiveTelemetryTab renders key selector and the live chart.
export default function LiveTelemetryTab({ deviceId }: LiveTelemetryTabProps) {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const subscribe = useWSStore((state) => state.subscribe);
  const livePoints = useWSStore((state) => state.points);

  // The inner sub-tab is reflected in ?view= so "View all" from the
  // strip and a refresh/opened shared link land on the same panel.
  const viewParam = searchParams.get("view");
  const activeView: innerViewKey = viewParam === "measurements"
    ? "measurements"
    : "chart";
  const changeView = (key: string) => {
    const next = new URLSearchParams(searchParams);
    if (key === "measurements") {
      next.set("view", "measurements");
    } else {
      next.delete("view");
    }
    setSearchParams(next, { replace: true });
  };

  // range bounds the seeded historical segment; live WS frames append
  // regardless of the window. A short default keeps the default series
  // query on a fine raw-hypertable scan, which is cheaper than a wide one.
  const [range, setRange] = useState<TelemetryRange>("5m");

  // Measurement keys are discovered from stored telemetry instead of
  // guessed from the current history page.
  const keysQuery = useQuery({
    queryKey: ["device-telemetry-keys", deviceId],
    queryFn: () => listTelemetryKeys(deviceId),
  });
  // Memoized so the merged-keys memo keeps a stable dependency identity
  // instead of seeing a fresh [] fallback on every render.
  const discoveredKeys = useMemo(() => keysQuery.data ?? [], [keysQuery.data]);

  // keys merges discovered keys with keys first appearing in live frames.
  const keys = useMemo(() => {
    const list: TelemetryKey[] = discoveredKeys.map((key) => ({ ...key }));
    const known = new Set(list.map((key) => key.measurement_key));
    for (const point of livePoints) {
      if (point.device_id !== deviceId || known.has(point.measurement_key)) {
        continue;
      }
      known.add(point.measurement_key);
      list.push({
        measurement_key: point.measurement_key,
        value_type: inferValueType(point),
        time: point.time,
      });
    }
    return list;
  }, [discoveredKeys, livePoints, deviceId]);

  const [selectedKey, setSelectedKey] = useState<string | undefined>();
  // String measurements have no chartable meaning, so the selector only
  // offers numeric/boolean keys; strings stay visible in All measurements.
  const chartKeys = useMemo(
    () => keys.filter((key) => key.value_type !== "string"),
    [keys],
  );
  const activeKey = chartKeys.find((key) => key.measurement_key === selectedKey)
    ? selectedKey
    : chartKeys[0]?.measurement_key;
  const activeMeta = chartKeys.find((key) => key.measurement_key === activeKey);

  // Downsampled series covers the whole selected window; a raw history
  // page only spans the newest rows regardless of range.
  const seriesQuery = useQuery({
    queryKey: ["device-telemetry-series", deviceId, activeKey, range],
    queryFn: () => listTelemetrySeries(deviceId, activeKey ?? "", range),
    enabled: !!activeKey,
  });
  // Memoized so the chart-series memo keeps a stable dependency identity
  // instead of seeing a fresh [] fallback on every render.
  const history: TelemetrySeriesPoint[] = useMemo(
    () => seriesQuery.data ?? [],
    [seriesQuery.data],
  );

  useEffect(() => {
    subscribe("device-telemetry", "telemetry", [deviceId]);
  }, [subscribe, deviceId]);

  // series merges the downsampled window (already key-filtered, ascending)
  // with live frames, keeping a bounded tail.
  const series = useMemo(() => {
    const rows: {
      time: string;
      type: TelemetryValueType;
      number?: number;
      boolean?: boolean;
      string?: string;
    }[] = [];
    for (const point of history) {
      const row: (typeof rows)[number] = { time: point.time, type: point.value_type };
      if (point.value_type === "boolean") row.boolean = point.v_bool ?? false;
      else if (point.value_type === "string") row.string = point.v_string ?? "";
      else row.number = point.v_double ?? 0;
      rows.push(row);
    }
    const seenTimes = new Set(rows.map((row) => row.time));
    for (const point of livePoints as TelemetryPoint[]) {
      if (point.device_id !== deviceId || point.measurement_key !== activeKey) {
        continue;
      }
      if (seenTimes.has(point.time)) continue;
      const type = inferValueType(point);
      const row: (typeof rows)[number] = { time: point.time, type };
      if (type === "boolean") row.boolean = point.v_bool ?? false;
      else if (type === "string") row.string = point.v_string ?? "";
      else row.number = point.v_double;
      rows.push(row);
    }
    return rows.slice(-MAX_CHART_POINTS);
  }, [history, livePoints, activeKey, deviceId]);

  const rangeOptions = useMemo(
    () =>
      rangePresets.map((value) => ({
        value,
        label: t(`pages.deviceDetail.telemetry.ranges.${value}`),
      })),
    [t],
  );

  const option = useMemo(
    () =>
      buildOption(
        series,
        activeMeta?.value_type ?? "number",
        activeMeta?.unit ?? "",
        t,
      ),
    [series, activeMeta, t],
  );

  return (
    <Tabs
      activeKey={activeView}
      onChange={changeView}
      items={[
        {
          key: "chart",
          label: t("pages.deviceDetail.tabs.chart"),
          children: (
            <Card>
              <Space style={{ marginBottom: 12, justifyContent: "space-between", width: "100%" }}>
                <Typography.Text strong>
                  {t("pages.deviceDetail.telemetry.heading")}
                </Typography.Text>
                <Space>
                  <Select
                    style={{ width: 120 }}
                    value={range}
                    onChange={(value) => setRange(value as TelemetryRange)}
                    options={rangeOptions}
                  />
                  <Select
                    style={{ width: 220 }}
                    placeholder={t("pages.deviceDetail.telemetry.selectMeasurement")}
                    value={activeKey}
                    onChange={setSelectedKey}
                    options={chartKeys.map((key) => ({
                      value: key.measurement_key,
                      label: `${key.measurement_key} (${key.value_type}${key.unit ? `, ${key.unit}` : ""})`,
                    }))}
                  />
                </Space>
              </Space>
              {chartKeys.length === 0 ? (
                <Empty description={t("pages.deviceDetail.telemetry.noChartableKeys")} />
              ) : series.length === 0 ? (
                <Empty description={t("pages.deviceDetail.telemetry.empty")} />
              ) : (
                <ReactECharts option={option} style={{ height: 380 }} notMerge />
              )}
            </Card>
          ),
        },
        {
          key: "measurements",
          label: t("pages.deviceDetail.tabs.allMeasurements"),
          children: <MeasurementsTable deviceId={deviceId} />,
        },
      ]}
    />
  );
}

// buildOption constructs the ECharts option for the active value type.
function buildOption(
  series: {
    time: string;
    type: TelemetryValueType;
    number?: number;
    boolean?: boolean;
    string?: string;
  }[],
  type: TelemetryValueType,
  unit: string,
  t: (key: string) => string,
) {
  const times = series.map((row) => new Date(row.time).toLocaleTimeString());

  if (type === "boolean") {    return {
      tooltip: { trigger: "axis" },
      grid: { left: 56, right: 24, top: 24, bottom: 48 },
      xAxis: { type: "category", data: times },
      yAxis: {
        type: "value",
        min: 0,
        max: 1,
        axisLabel: {
          formatter: (value: number) =>
            value === 1 ? t("pages.deviceDetail.telemetry.on") : t("pages.deviceDetail.telemetry.off"),
        },
      },
      series: [
        {
          type: "line",
          step: "end",
          showSymbol: false,
          data: series.map((row) => (row.boolean ? 1 : 0)),
          lineStyle: { width: 2 },
        },
      ],
    };
  }

  return {
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number | string) =>
        unit ? `${value} ${unit}` : `${value}`,
    },
    grid: { left: 56, right: 24, top: 24, bottom: 48 },
    xAxis: { type: "category", data: times },
    yAxis: { type: "value", scale: true, name: unit || undefined },
    series: [
      {
        type: "line",
        showSymbol: false,
        data: series.map((row) => row.number ?? 0),
        lineStyle: { width: 2 },
      },
    ],
  };
}
