// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StatWidget renders the latest value of one bound device key as a large
// number (ADR-0020). It reuses the shared useDeviceMeasurements merge so the
// value reflects live frames immediately.

import { useDeviceMeasurements } from "../../../hooks/useDeviceMeasurements";
import type { Widget } from "../../../api/dashboards";

// StatWidget renders a latest-value card.
export default function StatWidget({ widget }: { widget: Widget }) {
  const { keys } = useDeviceMeasurements(
    widget.device_id ?? "",
    `dash-stat-${widget.id}`,
  );
  const key = keys.find(
    (item) => item.measurement_key === widget.measurement_key,
  );

  const value =
    key?.value_type === "boolean"
      ? String(key.v_bool)
      : key?.value_type === "string"
        ? key.v_string
        : typeof key?.v_double === "number"
          ? formatNumber(key.v_double)
          : "--";

  return (
    <div
      style={{
        height: "100%",
        display: "flex",
        flexDirection: "column",
        justifyContent: "center",
        alignItems: "center",
        gap: 8,
      }}
    >
      <div style={{ fontSize: 40, fontWeight: 600, lineHeight: 1 }}>{value}</div>
      {key?.unit && <div style={{ color: "#888" }}>{key.unit}</div>}
    </div>
  );
}

// formatNumber trims trailing zeros for compact large-number display.
function formatNumber(value: number): string {
  if (Number.isInteger(value)) {
    return String(value);
  }
  return value.toFixed(2);
}
