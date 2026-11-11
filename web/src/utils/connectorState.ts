// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// connectorState.ts is the single color vocabulary for connector spec
// lifecycle states, shared by the gateway Connectors table and the
// connector column on the device list so both screens render a given
// state identically.

// ConnectorStateColor maps a spec apply state to an AntD tag color.
export const ConnectorStateColor: Record<string, string> = {
  PENDING: "default",
  DISPATCHED: "processing",
  CREATING: "processing",
  APPLIED: "success",
  DEGRADED: "warning",
  FAILED: "error",
};
