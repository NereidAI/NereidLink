// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// https://vitejs.dev/config/
export default defineConfig({
  define: {
    // Sidebar footer identity, baked at build time. Image builds inject the
    // immutable VITE_APP_VERSION (dev channel: 0.0.0-dev-<short-sha>,
    // releases: bare SemVer) via the Dockerfile; local npm builds fall back
    // to the package version so `npm run dev` still shows a real label.
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(
      process.env.VITE_APP_VERSION ?? process.env.npm_package_version ?? "dev",
    ),
  },
  plugins: [react()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/ws": {
        target: "ws://127.0.0.1:8080",
        ws: true,
      },
      "/v1": {
        target: "http://127.0.0.1:8080",
      },
      // Interactive API docs + embedded OpenAPI spec/assets are served by
      // the gateway under /api/docs; proxy them in development too.
      "/api": {
        target: "http://127.0.0.1:8080",
      },
    },
  },
});
