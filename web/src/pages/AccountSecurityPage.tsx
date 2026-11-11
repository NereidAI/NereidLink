// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// AccountSecurityPage manages the authenticated user's TOTP second factor.

import { useState } from "react";
import {
  App as AntApp,
  Alert,
  Badge,
  Button,
  Card,
  Descriptions,
  Drawer,
  Input,
  List,
  Space,
  Spin,
  Typography,
} from "antd";
import { QRCodeSVG } from "qrcode.react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import APIKeysCard from "../components/APIKeysCard";
import PageHeader from "../components/PageHeader";
import {
  disableTOTP,
  listSessions,
  mfaStatus,
  revokeSession,
  startTOTP,
  confirmTOTP,
  type RecoveryCodes,
  type TOTPEnrollment,
  type SessionInfo,
} from "../api/auth";
import { extractError } from "../api/client";
import { formatDateTime } from "../utils/format";

// AccountSecurityPage renders TOTP management and login sessions.
export default function AccountSecurityPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message, modal } = AntApp.useApp();
  const [enrollment, setEnrollment] = useState<TOTPEnrollment | null>(null);
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<RecoveryCodes | null>(null);

  const statusQuery = useQuery({
    queryKey: ["mfa-status"],
    queryFn: mfaStatus,
  });

  const sessionsQuery = useQuery({
    queryKey: ["auth-sessions"],
    queryFn: listSessions,
  });

  const startMutation = useMutation({
    mutationFn: startTOTP,
    onSuccess: (data) => {
      setEnrollment(data);
      setCode("");
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const confirmMutation = useMutation({
    mutationFn: () => confirmTOTP(code),
    onSuccess: async (data) => {
      setRecoveryCodes(data);
      setEnrollment(null);
      setCode("");
      await queryClient.invalidateQueries({ queryKey: ["mfa-status"] });
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const disableMutation = useMutation({
    mutationFn: disableTOTP,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["mfa-status"] });
      message.success(t("pages.accountSecurity.disabled"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const revokeMutation = useMutation({
    mutationFn: (sessionId: string) => revokeSession(sessionId),
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: ["auth-sessions"] });
      if (result.current) {
        message.warning(t("pages.accountSecurity.sessions.revokeCurrent"));
      } else {
        message.success(t("pages.accountSecurity.sessions.revoked"));
      }
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const enabled = statusQuery.data?.totp_enabled ?? false;

  const handleRevoke = (session: SessionInfo) => {
    if (session.current) {
      message.warning(t("pages.accountSecurity.sessions.revokeCurrent"));
      return;
    }
    modal.confirm({
      title: t("pages.accountSecurity.sessions.revokeConfirm"),
      onOk: () => revokeMutation.mutate(session.id),
    });
  };

  const renderSessions = () => {
    const sessions = sessionsQuery.data ?? [];
    if (sessions.length === 0) {
      return <Typography.Text type="secondary">{t("pages.accountSecurity.sessions.noSessions")}</Typography.Text>;
    }
    return (
      <List
        size="small"
        bordered
        dataSource={sessions}
        renderItem={(session: SessionInfo) => (
          <List.Item
            actions={session.current
              ? []
              : [
                  <Button
                    danger
                    size="small"
                    loading={revokeMutation.isPending}
                    onClick={() => handleRevoke(session)}
                  >
                    {t("pages.accountSecurity.sessions.revoke")}
                  </Button>,
                ]}
            extra={
              <>
                <Badge status={session.active ? "success" : "default"} text={t(`pages.accountSecurity.sessions.${session.active ? "active" : "inactive"}`)} />
                {session.current && (
                  <Badge status="processing" text={t("pages.accountSecurity.sessions.current")} style={{ marginLeft: 8 }} />
                )}
              </>
            }
          >
            <Space direction="vertical" size="small" style={{ width: "100%" }}>
              <Space>
                <strong>{session.user_agent || t("common.unknown")}</strong>
                <Typography.Text type="secondary" style={{ marginLeft: 8 }}>
                  {session.ip}
                </Typography.Text>
              </Space>
              <Space>
                <Typography.Text type="secondary">
                  {t("pages.accountSecurity.sessions.createdAt")}:{" "}
                  {formatDateTime(session.created_at)}
                </Typography.Text>
                <Typography.Text type="secondary">
                  {t("pages.accountSecurity.sessions.expiresAt")}:{" "}
                  {formatDateTime(session.expires_at)}
                </Typography.Text>
              </Space>
            </Space>
          </List.Item>
        )}
      />
    );
  };

  return (
    <>
      <PageHeader
        title={t("pages.accountSecurity.title")}
        subtitle={t("pages.accountSecurity.subtitle")}
        helpId="account-security"
      />
      <Card>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label={t("pages.accountSecurity.totp")}>
              {enabled ? t("pages.accountSecurity.enabled") : t("pages.accountSecurity.disabledLabel")}
            </Descriptions.Item>
          </Descriptions>

          {enabled ? (
            <Button danger onClick={() => disableMutation.mutate()}>
              {t("pages.accountSecurity.disable")}
            </Button>
          ) : enrollment == null ? (
            <Button type="primary" onClick={() => startMutation.mutate()}>
              {t("pages.accountSecurity.enable")}
            </Button>
          ) : (
            <Space direction="vertical" size="middle" style={{ maxWidth: 420 }}>
              <Alert
                type="info"
                showIcon
                message={t("pages.accountSecurity.scan")}
                description={
                  <Typography.Text copyable>{enrollment.secret}</Typography.Text>
                }
              />
              <div style={{ textAlign: "center" }}>
                <QRCodeSVG value={enrollment.uri} size={180} />
              </div>
              <Space.Compact style={{ width: "100%" }}>
                <Input
                  value={code}
                  placeholder={t("pages.accountSecurity.code")}
                  onChange={(event) => setCode(event.target.value)}
                  onPressEnter={() => confirmMutation.mutate()}
                />
                <Button
                  type="primary"
                  loading={confirmMutation.isPending}
                  disabled={!code.trim()}
                  onClick={() => confirmMutation.mutate()}
                >
                  {t("pages.accountSecurity.confirm")}
                </Button>
              </Space.Compact>
            </Space>
          )}
        </Space>
      </Card>

      <APIKeysCard />

      <Card title={t("pages.accountSecurity.sessions.title")}>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Typography.Paragraph>{t("pages.accountSecurity.sessions.subtitle")}</Typography.Paragraph>
          {sessionsQuery.isLoading ? <Spin /> : renderSessions()}
        </Space>
      </Card>

      <Drawer
        open={recoveryCodes !== null}
        width={720}
        title={t("pages.accountSecurity.recoveryTitle")}
        footer={
          <Space style={{ marginLeft: "auto" }}>
            <Button
              key="close"
              type="primary"
              onClick={() => setRecoveryCodes(null)}
            >
              {t("common.done")}
            </Button>
          </Space>
        }
        onClose={() => setRecoveryCodes(null)}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.accountSecurity.recoveryWarning")}
        />
        <List
          size="small"
          bordered
          dataSource={recoveryCodes?.codes ?? []}
          renderItem={(value) => <List.Item>{value}</List.Item>}
        />
      </Drawer>
    </>
  );
}
