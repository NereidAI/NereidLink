/// <reference types="vite/client" />

// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Vite replaces these at build time (see the define block in vite.config.ts).
// The footer identity is always defined; the interface makes the stamp
// compile-time visible instead of relying on the generic vite/client index.
interface ImportMetaEnv {
  readonly VITE_APP_VERSION: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
