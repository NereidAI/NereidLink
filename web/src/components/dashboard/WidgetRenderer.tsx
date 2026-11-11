// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// WidgetRenderer is the dashboard widget registry (ADR-0020): it maps a
// widget type to its component and wraps every widget in a titled frame
// that fills the grid item. A data widget without a binding shows a setup
// hint instead of rendering empty.

import { ReactNode } from "react";

import type { Widget } from "../../api/dashboards";
import TimeseriesWidget from "./widgets/TimeseriesWidget";
import StatWidget from "./widgets/StatWidget";
import AlarmsWidget from "./widgets/AlarmsWidget";

// WidgetRenderer renders one widget inside its frame.
export default function WidgetRenderer({ widget }: { widget: Widget }) {
  const needsBinding =
    widget.type !== "alarms" &&
    (!widget.device_id || !widget.measurement_key);

  return (
    <div style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div
        style={{
          padding: "6px 12px",
          fontWeight: 600,
          borderBottom: "1px solid #f0f0f0",
        }}
      >
        {widget.title || widget.measurement_key || widget.type}
      </div>
      <div style={{ flex: 1, minHeight: 0, padding: 8 }}>
        {needsBinding ? (
          <div style={{ color: "#bbb", textAlign: "center", paddingTop: 24 }}>
            ?
          </div>
        ) : (
          selectWidget(widget)
        )}
      </div>
    </div>
  );
}

// selectWidget maps a widget type to its component.
function selectWidget(widget: Widget): ReactNode {
  switch (widget.type) {
    case "timeseries":
      return <TimeseriesWidget widget={widget} />;
    case "stat":
      return <StatWidget widget={widget} />;
    case "alarms":
      return <AlarmsWidget widget={widget} />;
    default:
      return null;
  }
}
