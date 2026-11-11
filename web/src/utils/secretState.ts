// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secretState.ts is the single color vocabulary for secret delivery
// binding states, shared by the gateway Secrets table and any other view
// rendering a binding so a given state renders identically.

// SecretStateColor maps a binding status to an AntD tag color.
export const SecretStateColor: Record<string, string> = {
  PENDING: "default",
  DISPATCHED: "processing",
  APPLIED: "success",
  FAILED: "error",
  REMOVING: "warning",
  REMOVED: "default",
}
