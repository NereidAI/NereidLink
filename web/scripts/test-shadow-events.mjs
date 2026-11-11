// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression loop for live shadow consumers: a fresh runtime health frame must
// replace the cached document without requiring a route change or refetch.

import assert from "node:assert/strict";
import { reduceDeviceShadowEvents } from "../src/components/device/liveShadow.ts";

const gatewayId = "49a74ef6-271e-4a7c-83d9-dcff7f814eb2";
const shadowAt = (checkedAt, version) => ({
  device_id: gatewayId,
  desired: {},
  reported: {
    inference: {
      state: "ONLINE",
      checked_at: checkedAt,
      probe_interval_s: 30,
    },
  },
  delta: {},
  desired_version: 1,
  reported_version: version,
  etag: `"${version}"`,
  in_sync: true,
  updated_at: checkedAt,
});
const eventAt = (checkedAt, version, deviceId = gatewayId) => ({
  ...shadowAt(checkedAt, version),
  device_id: deviceId,
  tenant_id: "tenant-1",
  source: "device",
});

const beforeBaseline = eventAt("2026-11-01T00:00:00Z", 1);
const unrelated = eventAt("2026-11-01T00:00:25Z", 2, "another-device");
const older = eventAt("2026-11-01T00:00:20Z", 3);
const latest = eventAt("2026-11-01T00:00:30Z", 4);
const reduced = reduceDeviceShadowEvents(
  [beforeBaseline, unrelated, older, latest],
  1,
  gatewayId,
);
assert.equal(reduced.cursor, 4);
assert.equal(
  reduced.shadow.reported.inference.checked_at,
  "2026-11-01T00:00:30Z",
  "the newest matching runtime report must replace the stale cached timestamp",
);
assert.equal(reduced.shadow.reported_version, 4);
assert.equal("tenant_id" in reduced.shadow, false);
assert.equal("source" in reduced.shadow, false);

const noNewFrame = reduceDeviceShadowEvents(
  [beforeBaseline, unrelated, older, latest],
  reduced.cursor,
  gatewayId,
);
assert.equal(noNewFrame.cursor, 4);
assert.equal(noNewFrame.shadow, undefined);

const otherDeviceOnly = reduceDeviceShadowEvents([unrelated], 0, gatewayId);
assert.equal(otherDeviceOnly.shadow, undefined);
assert.equal(otherDeviceOnly.cursor, 1);

console.log("live shadow event regression checks passed");
