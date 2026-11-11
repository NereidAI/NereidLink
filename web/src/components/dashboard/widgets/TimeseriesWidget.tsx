// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// TimeseriesWidget renders a bound device key as a live line chart
// (ADR-0020). History is seeded from the telemetry series REST endpoint and
// live frames from the single WebSocket are appended, de-duplicated by
// timestamp. It subscribes under an id derived from the widget id so two
// charts never replace each other's server subscription.

import { useEffect, useMemo } from "react";
import ReactECharts from "echarts-for-react";
import { useQuery } from "@tanstack/react-query";

import {
  listTelemetrySeries,
  type TelemetryRange,
} from "../../../api/devices";
import type { Widget } from "../../../api/dashboards";
import { useWSStore, type TelemetryPoint } from "../../../store/wsStore";

// MAX_POINTS bounds the rendered series to keep the chart responsive.
const MAX_POINTS = 600;

// TimeseriesWidget renders the chart for one bound widget.
export default function TimeseriesWidget({ widget }: { widget: Widget }) {
  const livePoints = useWSStore((state) => state.points);
  const subscribe = useWSStore((state) => state.subscribe);
  const range = (widget.range || "1h") as TelemetryRange;
  const subscriptionId = `dash-timeseries-${widget.id}`;

  const seriesQuery = useQuery({
    queryKey: ["dash-series", widget.device_id, widget.measurement_key, range],
    queryFn: () =>
      listTelemetrySeries(
        widget.device_id ?? "",
        widget.measurement_key ?? "",
        range,
      ),
    enabled: !!widget.device_id && !!widget.measurement_key,
  });

  useEffect(() => {
    if (widget.device_id) {
      subscribe(subscriptionId, "telemetry", [widget.device_id]);
    }
  }, [subscribe, subscriptionId, widget.device_id]);

  const points = useMemo(() => {
    const byTime = new Map<string, number>();
    for (const point of seriesQuery.data ?? []) {
      if (typeof point.v_double === "number") {
        byTime.set(point.time, point.v_double);
      }
    }
    for (const point of livePoints as TelemetryPoint[]) {
      if (
        point.device_id === widget.device_id &&
        point.measurement_key === widget.measurement_key &&
        typeof point.v_double === "number"
      ) {
        byTime.set(point.time, point.v_double);
      }
    }
    return [...byTime.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .slice(-MAX_POINTS);
  }, [seriesQuery.data, livePoints, widget.device_id, widget.measurement_key]);

  const option = useMemo(
    () => ({
      grid: { top: 24, right: 16, bottom: 28, left: 48 },
      xAxis: {
        type: "category",
        data: points.map(([time]) => new Date(time).toLocaleTimeString()),
        axisLabel: { fontSize: 10 },
      },
      yAxis: { type: "value", scale: true, axisLabel: { fontSize: 10 } },
      tooltip: { trigger: "axis" },
      series: [
        {
          type: "line",
          showSymbol: false,
          data: points.map(([, value]) => value),
          lineStyle: { width: 1.5 },
        },
      ],
    }),
    [points],
  );

  return <ReactECharts option={option} style={{ height: "100%" }} notMerge />;
}
