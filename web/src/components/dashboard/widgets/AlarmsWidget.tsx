// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// AlarmsWidget renders the live (ACTIVE/ACK) alarms, either tenant-wide or
// scoped to the bound device (ADR-0020). Live alarm frames from the
// WebSocket are merged so newly raised/cleared alarms appear immediately.

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { listAlarms } from "../../../api/alarms";
import type { AlarmRow } from "../../../api/devices";
import type { Widget } from "../../../api/dashboards";
import type { AlarmEvent } from "../../../store/wsStore";
import { useWSStore } from "../../../store/wsStore";

// AlarmsWidget renders a compact live-alarm list.
export default function AlarmsWidget({ widget }: { widget: Widget }) {
  const liveAlarms = useWSStore((state) => state.alarms);

  const alarmsQuery = useQuery({
    queryKey: ["dash-alarms", widget.device_id],
    queryFn: () =>
      listAlarms({
        device_id: widget.device_id,
        state: "LIVE",
        page: 1,
        page_size: 50,
      }),
  });

  const rows = useMemo(() => {
    const byId = new Map<string, AlarmRow>();
    for (const alarm of alarmsQuery.data?.items ?? []) {
      byId.set(alarm.alarm_id, alarm);
    }
    for (const event of liveAlarms as AlarmEvent[]) {
      if (widget.device_id && event.device_id !== widget.device_id) {
        continue;
      }
      if (event.state === "CLEARED") {
        byId.delete(event.alarm_id);
        continue;
      }
      const previous = byId.get(event.alarm_id);
      byId.set(event.alarm_id, {
        ...(previous ?? defaultRow(event)),
        state: event.state,
        severity: event.severity,
        started_at: event.started_at,
      });
    }
    return [...byId.values()].sort((a, b) =>
      b.started_at.localeCompare(a.started_at),
    );
  }, [alarmsQuery.data, liveAlarms, widget.device_id]);

  return (
    <div style={{ height: "100%", overflow: "auto" }}>
      {rows.length === 0 && (
        <div style={{ color: "#999", textAlign: "center", paddingTop: 24 }}>
          -
        </div>
      )}
      {rows.map((row) => (
        <div
          key={row.alarm_id}
          style={{
            display: "flex",
            justifyContent: "space-between",
            gap: 8,
            padding: "6px 4px",
            borderBottom: "1px solid #f0f0f0",
            fontSize: 12,
          }}
        >
          <span
            style={{
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {row.alarm_type}
          </span>
          <span style={{ color: "#888", whiteSpace: "nowrap" }}>{row.state}</span>
        </div>
      ))}
    </div>
  );
}

// defaultRow builds a row from a live event when no stored row exists yet.
function defaultRow(event: AlarmEvent): AlarmRow {
  return {
    alarm_id: event.alarm_id,
    device_id: event.device_id,
    alarm_type: event.alarm_type,
    severity: event.severity,
    state: event.state,
    started_at: event.started_at,
  };
}
