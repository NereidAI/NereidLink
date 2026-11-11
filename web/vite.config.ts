// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// https://vitejs.dev/config/
export default defineConfig({
  define: {
    // Expose the package version to the sidebar footer at build time.
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(
      process.env.npm_package_version ?? "dev",
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
