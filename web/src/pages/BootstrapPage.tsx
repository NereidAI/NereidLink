// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BootstrapPage is the first-run administrator setup screen. On a fresh
// deployment the platform admin exists with no password, so the first
// visitor sets it here exactly once; on success the returned token pair is
// stored and the user lands in the console.

import { Button, Card, Form, Input, Result, Typography } from "antd";
import { LockOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useNavigate } from "react-router-dom";

import { completeBootstrap } from "../api/auth";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";

// bootstrapForm is the setup form shape.
interface bootstrapForm {
  password: string;
  confirm: string;
}

// BootstrapPage renders the first-run password creation screen.
export default function BootstrapPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [form] = Form.useForm<bootstrapForm>();
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const onFinish = async (values: bootstrapForm) => {
    setError("");
    setSubmitting(true);
    try {
      const pair = await completeBootstrap(values.password);
      authStore.getState().setSession(pair);
      navigate("/overview", { replace: true });
    } catch (err) {
      setError(extractError(err).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "#f0f2f5",
        padding: 16,
      }}
    >
      <Card style={{ width: 420 }} variant="borderless">
        <div style={{ textAlign: "center", marginBottom: 24 }}>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            {t("pages.bootstrap.title")}
          </Typography.Title>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {t("pages.bootstrap.subtitle")}
          </Typography.Paragraph>
        </div>
        <Form form={form} layout="vertical" onFinish={onFinish} requiredMark={false}>
          <Form.Item
            name="password"
            rules={[
              { required: true, message: t("pages.bootstrap.passwordRequired") },
              { min: 10, message: t("pages.bootstrap.passwordPolicy") },
            ]}
            hasFeedback
          >
            <Input.Password
              size="large"
              prefix={<LockOutlined />}
              placeholder={t("pages.bootstrap.passwordPlaceholder")}
              autoComplete="new-password"
            />
          </Form.Item>
          <Form.Item
            name="confirm"
            dependencies={["password"]}
            hasFeedback
            rules={[
              { required: true, message: t("pages.bootstrap.confirmRequired") },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue("password") === value) {
                    return Promise.resolve();
                  }
                  return Promise.reject(new Error(t("pages.bootstrap.confirmMismatch")));
                },
              }),
            ]}
          >
            <Input.Password
              size="large"
              prefix={<LockOutlined />}
              placeholder={t("pages.bootstrap.confirmPlaceholder")}
              autoComplete="new-password"
            />
          </Form.Item>
          {error ? (
            <Result
              status="error"
              title={t("pages.bootstrap.failed")}
              subTitle={error}
              style={{ padding: "8px 0 16px" }}
            />
          ) : null}
          <Button
            type="primary"
            htmlType="submit"
            size="large"
            block
            loading={submitting}
          >
            {t("pages.bootstrap.submit")}
          </Button>
        </Form>
      </Card>
    </div>
  );
}
