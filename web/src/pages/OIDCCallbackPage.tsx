// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OIDCCallbackPage is the public SPA route the backend redirects the browser
// to after the IdP authorization-code exchange. The local session token pair
// is delivered in the URL fragment (#session=...), which is never sent to a
// server or written to server logs; this page stores it and navigates away,
// replacing the history entry so the token does not linger in the URL.

import { Result, Spin } from "antd";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import type { TokenPair } from "../api/auth";
import { authStore } from "../store/authStore";

// callbackSession is the encoded fragment payload.
interface callbackSession extends TokenPair {
  redirect_to?: string;
}

export default function OIDCCallbackPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [error, setError] = useState("");
  const handled = useRef(false);

  useEffect(() => {
    if (handled.current) {
      return;
    }
    handled.current = true;
    try {
      const params = new URLSearchParams(window.location.hash.replace(/^#/, ""));
      const raw = params.get("session");
      if (!raw) {
        throw new Error("missing session");
      }
      const session = JSON.parse(raw) as callbackSession;
      if (!session.access_token || !session.refresh_token) {
        throw new Error("invalid session");
      }
      const { redirect_to: redirectTo, ...pair } = session;
      authStore.getState().setSession(pair);
      navigate(redirectTo && redirectTo.startsWith("/") ? redirectTo : "/overview", {
        replace: true,
      });
    } catch (err) {
      console.warn("oidc callback failed", err);
      // One-shot synchronization of the browser URL fragment with session
      // storage and the router: an external-sync effect whose only state
      // update is surfacing the failure Result.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setError("oidc_failed");
    }
  }, [navigate]);

  if (error) {
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
        <Result
          status="error"
          title={t("pages.login.oidcCallbackFailed")}
          subTitle={t(`pages.login.oidcError.${error}`, {
            defaultValue: t("pages.login.oidcError.oidc_failed"),
          })}
          extra={
            <a onClick={() => navigate("/login", { replace: true })} href="/login">
              {t("pages.login.oidcBackToLogin")}
            </a>
          }
        />
      </div>
    );
  }

  return (
    <div
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "#f0f2f5",
      }}
    >
      <Spin size="large" />
    </div>
  );
}
