// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ClaimTokenDrawer displays a freshly issued single-use claim token.
// The token is shown only here; the operator copies it for the
// installer. Closing the drawer does not revoke the token.

import { App as AntApp, Button, Drawer, Input, Space, Typography } from "antd";
import { CopyOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import type { ClaimToken } from "../api/ownership";

interface ClaimTokenDrawerProps {
  // open controls visibility.
  open: boolean;

  // token is the issued claim token result.
  token?: ClaimToken;

  // onClose hides the drawer.
  onClose: () => void;
}

// ClaimTokenDrawer renders the token result.
export default function ClaimTokenDrawer({
  open,
  token,
  onClose,
}: ClaimTokenDrawerProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();

  const copy = async () => {
    if (!token) {
      return;
    }
    await navigator.clipboard.writeText(token.token);
    message.success(t("pages.devices.toasts.tokenCopied"));
  };

  return (
    <Drawer
      open={open}
      width={720}
      title={t("pages.devices.lifecycle.tokenTitle")}
      onClose={onClose}
      footer={null}
    >
      <Typography.Paragraph type="secondary">
        {t("pages.devices.lifecycle.tokenHint")}
      </Typography.Paragraph>
      <Input.TextArea value={token?.token ?? ""} readOnly rows={4} />
      <Space style={{ marginTop: 12 }}>
        <Button icon={<CopyOutlined />} onClick={copy}>
          {t("pages.devices.lifecycle.copyToken")}
        </Button>
        <Typography.Text type="secondary">
          {t("pages.devices.lifecycle.expiresAt")}: {token?.expires_at}
        </Typography.Text>
      </Space>
      <Space style={{ marginTop: 12, display: "flex", justifyContent: "flex-end" }}>
        <Button onClick={onClose}>{t("common.close")}</Button>
      </Space>
    </Drawer>
  );
}
