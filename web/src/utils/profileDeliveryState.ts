// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profileDeliveryState.ts is the single color vocabulary for device-profile
// downlink states, shared by the profile distribution card and the gateway
// Profile delivery tab so a given state renders identically.

// ProfileDeliveryStateColor maps a delivery state to an AntD tag color.
// PENDING/DISPATCHED/REMOVING are platform-side only; the edge reports
// APPLIED/FAILED/REJECTED/REMOVED.
export const ProfileDeliveryStateColor: Record<string, string> = {
  PENDING: "default",
  DISPATCHED: "processing",
  APPLIED: "success",
  FAILED: "error",
  REJECTED: "warning",
  REMOVING: "warning",
  REMOVED: "default",
};

// pendingProfileDeliveryStates are the non-terminal states for which the
// gateway tab keeps polling so an edge-reported APPLIED/FAILED arrives without
// a manual refresh.
export const pendingProfileDeliveryStates = new Set([
  "PENDING",
  "DISPATCHED",
  "REMOVING",
]);
