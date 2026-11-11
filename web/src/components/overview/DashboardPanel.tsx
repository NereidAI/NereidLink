// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DashboardPanel renders one titled card inside a react-grid-layout item.
// The body fills the available item height so resize handles control the
// visible chart area.

import type { ReactNode } from "react";
import { Card, Typography } from "antd";

interface DashboardPanelProps {
  // title is the panel heading.
  title: string;

  // children fills the panel body.
  children: ReactNode;

  // extra shows optional header actions.
  extra?: ReactNode;
}

// DashboardPanel renders the card container.
export default function DashboardPanel({
  title,
  children,
  extra,
}: DashboardPanelProps) {
  return (
    <Card
      size="small"
      style={{ height: "100%", borderRadius: 8 }}
      styles={{ body: { height: "calc(100% - 39px)", overflow: "auto" } }}
      title={<Typography.Text strong>{title}</Typography.Text>}
      extra={extra}
    >
      {children}
    </Card>
  );
}
