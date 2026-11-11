// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// PageHeader renders the standard list/detail page heading card: title,
// optional subtitle and a right-aligned action area.

import { Button, Tooltip, Typography } from "antd";
import { QuestionCircleOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";

import { useHelpStore } from "../help/helpStore";

interface PageHeaderProps {
  // Title is the page heading.
  title: string;

  // Subtitle optionally describes the page.
  subtitle?: string;

  // extra holds right-aligned action controls.
  extra?: ReactNode;

  // helpId opens the matching help document when supplied.
  helpId?: string;
}

// PageHeader renders the heading row inside a white card.
export default function PageHeader({ title, subtitle, extra, helpId }: PageHeaderProps) {
  const { t } = useTranslation();
  const openHelp = useHelpStore((state) => state.open);

  return (
    <div
      style={{
        marginBottom: 16,
        background: "#fff",
        padding: "16px 24px",
        borderRadius: 8,
        display: "flex",
        justifyContent: "space-between",
        alignItems: "center",
        flexWrap: "wrap",
        gap: 16,
      }}
    >
      <div>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {title}
        </Typography.Title>
        {subtitle && (
          <Typography.Text type="secondary">{subtitle}</Typography.Text>
        )}
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        {extra}
        {helpId && (
          <Tooltip title={t("nav.help")}>
            <Button
              type="text"
              icon={<QuestionCircleOutlined />}
              onClick={() => openHelp(helpId)}
            />
          </Tooltip>
        )}
      </div>
    </div>
  );
}
