// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// liveShadow.ts reduces the rolling WebSocket shadow buffer for one device.
// It is deliberately independent of React so cache-freshness regressions can
// exercise the exact event-selection and full-document replacement behavior.

import type { Shadow } from "../../api/shadow";
import type { ShadowEvent } from "../../store/wsStore";

// DeviceShadowReduction carries the advanced buffer cursor and, when a new
// frame matched, the complete shadow document to cache.
export interface DeviceShadowReduction {
  cursor: number;
  shadow?: Shadow;
}

// reduceDeviceShadowEvents selects only frames appended after the consumer's
// baseline. The server sends a complete post-commit shadow, so the newest
// matching frame replaces the cached document instead of merging stale fields.
export function reduceDeviceShadowEvents(
  events: ShadowEvent[],
  cursor: number,
  deviceId: string,
): DeviceShadowReduction {
  const fresh = events
    .slice(cursor)
    .filter((event) => event.device_id === deviceId);
  const latest = fresh[fresh.length - 1];
  if (!latest) return { cursor: events.length };
  return {
    cursor: events.length,
    shadow: {
      device_id: latest.device_id,
      desired: latest.desired,
      reported: latest.reported,
      delta: latest.delta,
      desired_version: latest.desired_version,
      reported_version: latest.reported_version,
      etag: latest.etag,
      in_sync: latest.in_sync,
      desired_at: latest.desired_at,
      reported_at: latest.reported_at,
      updated_at: latest.updated_at,
    },
  };
}
