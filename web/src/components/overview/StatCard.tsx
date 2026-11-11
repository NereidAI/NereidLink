// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StatCard renders one compact KPI with an icon, value and helper text.
// When a target route is supplied, the card becomes a navigation link.

import { Card, Statistic, Typography } from "antd";
import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";

interface StatCardProps {
  // title labels the metric.
  title: string;

  // value is the current metric value.
  value: number | string;

  // suffix is appended to the value.
  suffix?: string;

  // icon provides a visual cue.
  icon: ReactNode;

  // helper provides secondary context.
  helper?: string;

  // tone colors the icon and value.
  tone: string;

  // to optionally makes the card a navigation link to the given route.
  to?: string;
}

// StatCard renders the KPI card.
export default function StatCard({
  title,
  value,
  suffix,
  icon,
  helper,
  tone,
  to,
}: StatCardProps) {
  const navigate = useNavigate();

  return (
    <Card
      size="small"
      hoverable={Boolean(to)}
      onClick={to ? () => navigate(to) : undefined}
      style={{
        borderRadius: 8,
        height: "100%",
        cursor: to ? "pointer" : "default",
      }}
      styles={{ body: { display: "flex", alignItems: "center", gap: 12 } }}
    >
      <div
        style={{
          width: 44,
          height: 44,
          borderRadius: 10,
          background: `${tone}18`,
          color: tone,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          fontSize: 20,
          flexShrink: 0,
        }}
      >
        {icon}
      </div>
      <div style={{ minWidth: 0 }}>
        <Statistic
          title={title}
          value={value}
          suffix={suffix}
          valueStyle={{ fontSize: 22, color: tone, lineHeight: 1.2 }}
        />
        {helper && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {helper}
          </Typography.Text>
        )}
      </div>
    </Card>
  );
}
