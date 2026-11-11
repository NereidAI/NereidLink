// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StatusBadge renders the standard state color vocabulary: lifecycle states
// across devices, commands, alarms and rule chains share one color mapping so
// identical meanings never differ visually between pages. It is a plain,
// self-explanatory label: callers that want extra context (e.g. a timestamp)
// wrap it in their own Tooltip rather than the badge repeating its own text
// on hover.

import { forwardRef } from "react";
import { Tag } from "antd";
import { useTranslation } from "react-i18next";

// BadgeColor is the subset of AntD Tag colors used for state semantics.
type BadgeColor =
  | "gray"
  | "blue"
  | "green"
  | "red"
  | "volcano"
  | "orange"
  | "cyan";

// statusColors maps each lower-cased state to its semantic color.
const statusColors: Record<string, BadgeColor> = {
  // Generic lifecycle states.
  pending: "gray",
  queued: "blue",
  running: "blue",
  claimed: "blue",
  completed: "green",
  active: "green",
  ready: "green",
  healthy: "green",
  online: "green",
  delivered: "green",
  succeeded: "green",
  cleared: "green",
  failed: "red",
  unhealthy: "red",
  offline: "red",
  timeout: "red",
  cancelled: "volcano",
  expired: "volcano",
  not_connected: "volcano",
  ack: "cyan",
  deployed: "cyan",
  maintenance: "orange",
  draft: "gray",
  archived: "volcano",
  quarantined: "volcano",
  unknown: "gray",
};

interface StatusBadgeProps {
  // Status is the raw (typically upper-case) state from the API.
  status: string;
}

// StatusBadge renders a translated, color-coded state tag. It forwards its
// ref to the Tag so a wrapping Tooltip can attach to it directly instead of
// falling back to the deprecated findDOMNode.
const StatusBadge = forwardRef<HTMLSpanElement, StatusBadgeProps>(
  function StatusBadge({ status }, ref) {
    const { t } = useTranslation();
    const key = status.toLowerCase();
    const color = statusColors[key] ?? "gray";
    const label = t(`status.${camelToKey(key)}`, { defaultValue: status });

    return (
      <Tag ref={ref} color={color}>
        {label}
      </Tag>
    );
  },
);

export default StatusBadge;

// camelToKey maps enum-style values to locale key suffixes, e.g.
// "not_connected" -> "notConnected".
function camelToKey(snake: string): string {
  const parts = snake.split("_");
  return parts
    .map((part, index) =>
      index === 0
        ? part
        : part.charAt(0).toUpperCase() + part.slice(1),
    )
    .join("");
}
