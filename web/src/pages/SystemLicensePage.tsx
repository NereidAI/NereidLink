// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SystemLicensePage is the platform super-administrator console for the
// deployment license (ADR-0024). It shows the installed deployment license
// and its effective gate state and lets the administrator upload a
// vendor-signed license envelope (the same file accepted by
// `nereidlink license install`). The server verifies the Ed25519 signature
// and rejects anything other than a deployment-scope license.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Descriptions,
  Drawer,
  Form,
  Input,
  Space,
  Spin,
  Tag,
  Typography,
  Upload,
} from "antd";
import { InboxOutlined, UploadOutlined } from "@ant-design/icons";
import type { UploadProps } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import PageHeader from "../components/PageHeader";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";
import {
  getDeploymentLicense,
  installDeploymentLicense,
  type DeploymentLicense,
  type LicenseEnvelope,
} from "../api/license";

// formatTime renders an optional RFC3339 timestamp or a dash.
function formatTime(value?: string): string {
  return value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "—";
}

// capText renders a cap where zero means unlimited.
function capText(value: number, unlimited: string): string {
  return value > 0 ? String(value) : unlimited;
}

// SystemLicensePage renders the deployment license status and upload drawer.
export default function SystemLicensePage() {
  const { t } = useTranslation();
  const user = authStore((state) => state.user);
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<{ envelope: string }>();

  const query = useQuery({
    queryKey: ["platform-license"],
    queryFn: getDeploymentLicense,
  });
  const info: DeploymentLicense | undefined = query.data;

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["platform-license"] });

  const installMutation = useMutation({
    mutationFn: installDeploymentLicense,
    onSuccess: async () => {
      await invalidate();
      setOpen(false);
      message.success(t("pages.systemLicense.toasts.installed"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  if (!user?.is_platform_admin) {
    return (
      <>
        <PageHeader title={t("pages.systemLicense.title")} />
        <Alert type="error" showIcon message={t("pages.systemLicense.forbidden")} />
      </>
    );
  }

  // uploadProps loads a selected file's text into the hidden form field.
  // beforeUpload returning false makes Ant Design skip its own XHR upload so
  // the envelope is only submitted with the Install button.
  const uploadProps: UploadProps = {
    accept: "application/json,.json",
    maxCount: 1,
    showUploadList: false,
    beforeUpload: (file) => {
      const reader = new FileReader();
      reader.onload = () => form.setFieldValue("envelope", String(reader.result ?? ""));
      reader.onerror = () => message.error(t("pages.systemLicense.toasts.readFailed"));
      reader.readAsText(file);
      return false;
    },
  };

  const submit = (values: { envelope: string }) => {
    let parsed: LicenseEnvelope;
    try {
      parsed = JSON.parse(values.envelope) as LicenseEnvelope;
    } catch {
      message.error(t("pages.systemLicense.toasts.invalidJson"));
      return;
    }
    if (!parsed.payload || !parsed.signature) {
      message.error(t("pages.systemLicense.toasts.missingFields"));
      return;
    }
    installMutation.mutate(parsed);
  };

  return (
    <>
      <PageHeader
        title={t("pages.systemLicense.title")}
        subtitle={t("pages.systemLicense.subtitle")}
        helpId="system-license"
        extra={
          <Button type="primary" icon={<UploadOutlined />} onClick={() => setOpen(true)}>
            {t("pages.systemLicense.import")}
          </Button>
        }
      />

      {info?.read_only && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("pages.systemLicense.readOnlyAlert")}
          description={info.reason}
        />
      )}

      <Spin spinning={query.isFetching}>
        <Descriptions bordered column={1} size="middle">
          <Descriptions.Item label={t("pages.systemLicense.fields.status")}>
            {info?.installed ? (
              <Tag color="green">{t("pages.systemLicense.statuses.installed")}</Tag>
            ) : (
              <Tag color="red">{t("pages.systemLicense.statuses.notInstalled")}</Tag>
            )}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.customer")}>
            {info?.customer_name || "—"}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.tenantCap")}>
            {info ? capText(info.tenant_cap, t("pages.systemLicense.unlimited")) : "—"}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.tenantCount")}>
            {info?.tenant_count ?? "—"}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.issuedAt")}>
            {formatTime(info?.issued_at)}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.expiresAt")}>
            {formatTime(info?.expires_at)}
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.systemLicense.fields.features")}>
            <Space size={4} wrap>
              {(info?.features ?? []).length === 0 ? (
                <Typography.Text type="secondary">
                  {t("pages.systemLicense.noFeatures")}
                </Typography.Text>
              ) : (
                info?.features.map((feature) => <Tag key={feature}>{feature}</Tag>)
              )}
            </Space>
          </Descriptions.Item>
        </Descriptions>
      </Spin>

      <Drawer
        open={open}
        title={t("pages.systemLicense.importTitle")}
        width={720}
        onClose={() => setOpen(false)}
        footer={null}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" preserve={false} onFinish={submit}>
          <Upload.Dragger {...uploadProps} style={{ marginBottom: 12 }}>
            <p className="ant-upload-drag-icon">
              <InboxOutlined />
            </p>
            <p className="ant-upload-text">{t("pages.systemLicense.chooseFile")}</p>
          </Upload.Dragger>
          <Form.Item
            label={t("pages.systemLicense.form.envelope")}
            name="envelope"
            tooltip={t("pages.systemLicense.form.envelopeTip")}
            rules={[
              {
                required: true,
                message: t("pages.systemLicense.form.envelopeRequired"),
              },
            ]}
          >
            <Input.TextArea rows={10} spellCheck={false} />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setOpen(false)}>{t("common.cancel")}</Button>
              <Button type="primary" htmlType="submit" loading={installMutation.isPending}>
                {t("pages.systemLicense.install")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
}
