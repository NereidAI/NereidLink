// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OperationDetailDrawer shows one effective operation descriptor read-only.
// Edge-advertised descriptors cannot be edited in place; the drawer offers a
// clone action instead. Private descriptors link out to the editor.

import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Input,
  Space,
  Tag,
  Typography,
} from "antd";
import { CopyOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  cloneOperation,
  type OperationView,
} from "../api/gatewayOperations";
import { extractError } from "../api/client";

interface OperationDetailDrawerProps {
  open: boolean;
  target: OperationView | null;
  onClose: () => void;
  onEdit: (row: OperationView) => void;
  onCloned: () => void;
}

// riskColor maps risk levels to tag colors.
function riskColor(risk: string): string {
  if (risk === "dangerous") return "red";
  if (risk === "normal") return "orange";
  return "blue";
}

// pretty renders JSON for read-only display.
function pretty(value: Record<string, unknown> | undefined): string {
  return JSON.stringify(value ?? {}, null, 2);
}

// localizedLabel resolves a {en,zh} map by UI language with en/empty fallback.
function localizedLabel(
  map: Record<string, string> | undefined,
  lang: string,
): string {
  const key = lang.startsWith("zh") ? "zh" : "en";
  return map?.[key] || map?.en || "";
}

// OperationDetailDrawer renders the read-only descriptor.
export default function OperationDetailDrawer({
  open,
  target,
  onClose,
  onEdit,
  onCloned,
}: OperationDetailDrawerProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();

  const cloneMutation = useMutation({
    mutationFn: (row: OperationView) =>
      cloneOperation(row.operation, row.type_version, `${row.operation}-copy`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["gateway-operations"] });
      onCloned();
    },
  });

  if (!target) return null;

  const edge = target.source === "edge";

  return (
    <Drawer
      width={560}
      open={open}
      onClose={onClose}
      destroyOnClose
      title={localizedLabel(target.display_name, i18n.language) || target.operation}
      extra={
        <Space>
          {edge ? (
            <Button
              type="primary"
              icon={<CopyOutlined />}
              loading={cloneMutation.isPending}
              onClick={() => cloneMutation.mutate(target)}
            >
              {t("pages.operations.clone")}
            </Button>
          ) : (
            <Button type="primary" onClick={() => onEdit(target)}>
              {t("common.edit")}
            </Button>
          )}
        </Space>
      }
    >
      {cloneMutation.isError && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={extractError(cloneMutation.error).message}
        />
      )}
      {edge && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("pages.operations.detail.edgeReadonly")}
        />
      )}

      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label={t("pages.operations.form.operation")}>
          <Typography.Text code>{target.operation}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.version")}>
          {target.type_version || "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.risk")}>
          <Tag color={riskColor(target.risk)}>
            {t(`pages.operations.risks.${target.risk}`)}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.interaction")}>
          {t(
            target.interaction === "one-way"
              ? "pages.operations.interaction.ow"
              : "pages.operations.interaction.rr",
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.timeout")}>
          {t("pages.operations.table.timeoutMs", { ms: target.timeout_ms })}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.detail.source")}>
          <Space direction="vertical" size={4}>
            <Space wrap>
              <Tag>{t(`pages.operations.sources.${edge ? "edge" : "manual"}`)}</Tag>
              {target.disabled && (
                <Tag color="red">{t("pages.operations.detail.disabled")}</Tag>
              )}
              {target.conflict && (
                <Tag color="red">{t("pages.operations.conflict")}</Tag>
              )}
            </Space>
            {edge && target.advertised_by && target.advertised_by.length > 0 && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t("pages.operations.advertisedBy", {
                  count: target.gateway_count ?? target.advertised_by.length,
                })}
                {": "}
                {target.advertised_by.map((g) => g.name).join(", ")}
              </Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.labelEn")}>
          {target.display_name?.en || "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.labelZh")}>
          {target.display_name?.zh || "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.descEn")}>
          {target.description?.en || "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.operations.form.descZh")}>
          {target.description?.zh || "-"}
        </Descriptions.Item>
      </Descriptions>

      <Typography.Title level={5} style={{ marginTop: 20 }}>
        {t("pages.operations.form.paramSchema")}
      </Typography.Title>
      <Input.TextArea readOnly autoSize={{ minRows: 4, maxRows: 14 }} value={pretty(target.param_schema)} />

      <Typography.Title level={5} style={{ marginTop: 16 }}>
        {t("pages.operations.form.resultSchema")}
      </Typography.Title>
      <Input.TextArea readOnly autoSize={{ minRows: 4, maxRows: 14 }} value={pretty(target.result_schema)} />
    </Drawer>
  );
}
