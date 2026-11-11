// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// main.tsx bootstraps the providers shared by the whole application:
// i18n, TanStack Query, AntD theme/locale and React Router.
//
// The AntD locale is kept in a small wrapper so it re-renders immediately
// when the user switches language instead of requiring a reload.

import React from "react";
import ReactDOM from "react-dom/client";
import { App as AntdApp, ConfigProvider } from "antd";
import enUS from "antd/locale/en_US";
import zhCN from "antd/locale/zh_CN";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import { useTranslation } from "react-i18next";

import App from "./App";
import "./i18n";
import "./styles/global.css";

// queryClient holds one shared client; queries avoid refetch-on-focus since
// real-time freshness is delivered over WebSocket.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 60_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

// ThemedApp binds the reactive AntD locale to the i18n language.
function ThemedApp() {
  const { i18n } = useTranslation();
  const locale = i18n.language?.startsWith("zh") ? zhCN : enUS;
  return (
    <ConfigProvider
      locale={locale}
      theme={{ token: { colorPrimary: "#1677ff", borderRadius: 6 } }}
    >
      <AntdApp>
        <App />
      </AntdApp>
    </ConfigProvider>
  );
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <ThemedApp />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
