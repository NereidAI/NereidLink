// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// FirstRunRoute decides which public screen an unauthenticated visitor sees:
// while the platform admin password is unset it renders the first-run
// BootstrapPage; otherwise it renders the normal LoginPage. The status
// endpoint is public.

import { Spin } from "antd";
import { useEffect, useState } from "react";

import { getBootstrapStatus } from "../api/auth";
import BootstrapPage from "./BootstrapPage";
import LoginPage from "./LoginPage";

export default function FirstRunRoute() {
  const [required, setRequired] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    getBootstrapStatus()
      .then((status) => {
        if (active) setRequired(status.required);
      })
      .catch(() => {
        // If the probe fails, fall back to the login screen rather than
        // blocking access; a real login error surfaces there.
        if (active) setRequired(false);
      });
    return () => {
      active = false;
    };
  }, []);

  if (required === null) {
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
  return required ? <BootstrapPage /> : <LoginPage />;
}
