// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// LoginPage is the local identity fallback screen (ADR-0021): email +
// password, centered card. A server-issued arithmetic captcha gates the
// password login, and a language switch is offered before authentication.
// On success the session is stored and the user is sent to the page they
// originally requested.

import {
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  Row,
  Select,
  Typography,
} from "antd";
import {
  LockOutlined,
  LoginOutlined,
  MailOutlined,
  SafetyOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { useCallback, useEffect, useState } from "react";

import {
  getCaptcha,
  getOIDCStatus,
  getSSOStatus,
  login,
  startOIDCLogin,
  verifyMFA,
} from "../api/auth";
import type { CaptchaChallenge } from "../api/auth";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";

// loginForm is the credential form shape.
interface loginForm {
  email: string;
  password: string;
  code?: string;
  captcha?: string;
}

// languageOptions are the pre-auth selectable interface languages.
const languageOptions = [
  { value: "en", label: "English" },
  { value: "zh", label: "中文" },
];

// LoginPage renders the login screen.
export default function LoginPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams] = useSearchParams();
  const [form] = Form.useForm<loginForm>();
  // The OIDC/SSO error code arrives as a URL search parameter at mount; it
  // is initial state, not an effect-driven update, so resolve it in the
  // lazy initializer.
  const [error, setError] = useState(() => {
    const oidcError = searchParams.get("oidc_error");
    if (oidcError) {
      return t(`pages.login.oidcError.${oidcError}`, {
        defaultValue: t("pages.login.oidcError.oidc_failed"),
      });
    }
    const ssoError = searchParams.get("sso_error");
    if (ssoError) {
      return t(`pages.login.ssoError.${ssoError}`, {
        defaultValue: t("pages.login.ssoError.sso_failed"),
      });
    }
    return "";
  });
  const [challengeToken, setChallengeToken] = useState("");
  const [captcha, setCaptcha] = useState<CaptchaChallenge | null>(null);
  const [oidcEnabled, setOidcEnabled] = useState(false);
  const [ssoLoginURL, setSsoLoginURL] = useState("");

  const target =
    (location.state as { returnTo?: string } | null)?.returnTo ?? "/overview";

  useEffect(() => {
    getOIDCStatus()
      .then((status) => setOidcEnabled(status.enabled))
      .catch(() => setOidcEnabled(false));
    getSSOStatus()
      .then((status) => {
        if (status.enabled && status.login_url) {
          setSsoLoginURL(status.login_url);
        }
      })
      .catch(() => undefined);
    // Run once on mount; only module-level fetch helpers are referenced.
  }, []);

  // refreshCaptcha loads a fresh one-time challenge and clears any prior
  // answer; called on mount and after every failed attempt.
  const refreshCaptcha = useCallback(async () => {
    try {
      const challenge = await getCaptcha();
      setCaptcha(challenge);
      form.setFieldValue("captcha", undefined);
    } catch {
      setCaptcha(null);
    }
  }, [form]);

  // Fetch the initial challenge on mount with a cancellation guard so a
  // StrictMode double-invocation cannot apply a stale response. Failed
  // login retries reuse refreshCaptcha from the submit handler.
  useEffect(() => {
    let cancelled = false;
    getCaptcha()
      .then((challenge) => {
        if (cancelled) return;
        setCaptcha(challenge);
        form.setFieldValue("captcha", undefined);
      })
      .catch(() => {
        if (!cancelled) setCaptcha(null);
      });
    return () => {
      cancelled = true;
    };
  }, [form]);

  const onFinish = async (values: loginForm) => {
    setError("");
    try {
      if (challengeToken) {
        const pair = await verifyMFA(challengeToken, values.code ?? "");
        authStore.getState().setSession(pair);
        navigate(target, { replace: true });
        return;
      }

      const answer = Number(values.captcha);
      const result = await login(
        values.email,
        values.password,
        captcha?.captcha_id ?? "",
        Number.isNaN(answer) ? -1 : answer,
      );
      if (result.mfa) {
        setChallengeToken(result.mfa.challenge_token);
        return;
      }
      if (result.pair) {
        authStore.getState().setSession(result.pair);
        navigate(target, { replace: true });
      }
    } catch (err) {
      setError(extractError(err).message);
      void refreshCaptcha();
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
      <Card style={{ width: 380, position: "relative" }} variant="borderless">
        <Select
          size="small"
          value={i18n.language?.startsWith("zh") ? "zh" : "en"}
          onChange={(value) => void i18n.changeLanguage(value)}
          options={languageOptions}
          style={{ position: "absolute", top: 16, right: 16, width: 104 }}
        />
        <div style={{ textAlign: "center", marginBottom: 24 }}>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            {t("pages.login.title")}
          </Typography.Title>
          <Typography.Text type="secondary">
            {t("pages.login.subtitle")}
          </Typography.Text>
        </div>
        <Form
          form={form}
          layout="vertical"
          onFinish={onFinish}
          requiredMark={false}
        >
          <Form.Item
            name="email"
            rules={[
              { required: true, message: t("pages.login.emailRequired") },
              { type: "email", message: t("pages.login.emailInvalid") },
            ]}
          >
            <Input
              size="large"
              prefix={<MailOutlined />}
              placeholder={t("pages.login.emailPlaceholder")}
              autoComplete="username"
            />
          </Form.Item>
          <Form.Item
            name="password"
            rules={[{ required: true, message: t("pages.login.passwordRequired") }]}
          >
            <Input.Password
              size="large"
              prefix={<LockOutlined />}
              placeholder={t("pages.login.passwordPlaceholder")}
              autoComplete="current-password"
            />
          </Form.Item>
          {challengeToken ? (
            <Form.Item
              name="code"
              rules={[{ required: true, message: t("pages.login.codeRequired") }]}
            >
              <Input
                size="large"
                prefix={<LockOutlined />}
                placeholder={t("pages.login.codePlaceholder")}
                autoComplete="one-time-code"
              />
            </Form.Item>
          ) : (
            <Row gutter={8} style={{ marginBottom: 16 }}>
              <Col span={15}>
                <Form.Item
                  name="captcha"
                  rules={[
                    {
                      required: true,
                      message: t("pages.login.captchaRequired"),
                    },
                    {
                      pattern: /^\d+$/,
                      message: t("pages.login.captchaRequired"),
                    },
                  ]}
                  style={{ marginBottom: 0 }}
                >
                  <Input
                    size="large"
                    prefix={
                      <>
                        <SafetyOutlined />
                        <span
                          title={t("pages.login.captchaHint")}
                          style={{ marginLeft: 8, color: "rgba(0,0,0,0.65)" }}
                        >
                          {captcha?.expression ?? "..."}
                        </span>
                      </>
                    }
                    placeholder={t("pages.login.captchaPlaceholder")}
                    inputMode="numeric"
                    autoComplete="off"
                  />
                </Form.Item>
              </Col>
              <Col span={9}>
                <Button size="large" block onClick={() => void refreshCaptcha()}>
                  {t("pages.login.refreshCaptcha")}
                </Button>
              </Col>
            </Row>
          )}
          {error ? (
            <Typography.Text type="danger" style={{ display: "block", marginBottom: 12 }}>
              {error}
            </Typography.Text>
          ) : null}
          <Button type="primary" htmlType="submit" size="large" block>
            {t("pages.login.submit")}
          </Button>
          {oidcEnabled && !challengeToken ? (
            <>
              <Divider plain style={{ margin: "16px 0" }}>
                <Typography.Text type="secondary">
                  {t("pages.login.oidcOr")}
                </Typography.Text>
              </Divider>
              <Button
                size="large"
                block
                icon={<LoginOutlined />}
                onClick={() => startOIDCLogin(target)}
              >
                {t("pages.login.oidcSubmit")}
              </Button>
            </>
          ) : null}
          {ssoLoginURL && !challengeToken ? (
            <>
              <Divider plain style={{ margin: "16px 0" }}>
                <Typography.Text type="secondary">
                  {t("pages.login.ssoOr")}
                </Typography.Text>
              </Divider>
              <Button
                size="large"
                block
                icon={<LoginOutlined />}
                href={ssoLoginURL}
              >
                {t("pages.login.ssoSubmit")}
              </Button>
            </>
          ) : null}
        </Form>
      </Card>
    </div>
  );
}
