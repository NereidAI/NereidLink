// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ValidationIssues renders server-authoritative document and preflight issues
// without converting warnings into blocking errors or synthesizing missing
// node/artifact locations.

import { List, Space, Tag, Typography } from "antd";
import { useTranslation } from "react-i18next";

import type { ValidationIssue } from "../../api/business";

interface ValidationIssuesProps {
  issues: ValidationIssue[];
  emptyText?: string;
}

// ValidationIssues lists issue codes, messages, severity, and source IDs.
export default function ValidationIssues({ issues, emptyText }: ValidationIssuesProps) {
  const { t } = useTranslation();
  return (
    <List
      size="small"
      dataSource={issues}
      locale={{ emptyText: emptyText ?? t("business.validation.noIssues") }}
      renderItem={(issue, index) => (
        <List.Item key={`${issue.code}-${issue.node_id ?? issue.artifact_key ?? index}`}>
          <Space direction="vertical" size={4} style={{ width: "100%" }}>
            <Space wrap>
              <Tag color={issue.blocking ? "error" : "warning"}>
                {issue.blocking
                  ? t("business.validation.blocking")
                  : t("business.validation.warning")}
              </Tag>
              <Typography.Text code>{issue.code}</Typography.Text>
            </Space>
            <Typography.Text>{issue.message}</Typography.Text>
            {issue.node_id && (
              <Typography.Text type="secondary">
                {t("business.validation.node")}: {issue.node_id}
              </Typography.Text>
            )}
            {issue.artifact_key && (
              <Typography.Text type="secondary">
                {t("business.validation.artifact")}: {issue.artifact_key}
              </Typography.Text>
            )}
          </Space>
        </List.Item>
      )}
    />
  );
}
