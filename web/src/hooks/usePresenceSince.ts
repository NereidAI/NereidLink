// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// usePresenceSince derives the latest presence boundary per device from
// the presence rolling buffer, keeping both state and boundary time.
// useOnlineMap only keeps states; callers that display how long a device
// has been online/offline need the time as well.

import { useMemo } from "react";

import { useWSStore } from "../store/wsStore";

// PresenceSince is one device's latest boundary.
export interface PresenceSince {
  // State is ONLINE or OFFLINE.
  state: string;

  // Time is the boundary time as RFC3339 UTC.
  time: string;
}

// usePresenceSince returns deviceID -> latest boundary.
export function usePresenceSince(): Map<string, PresenceSince> {
  const presence = useWSStore((state) => state.presence);

  return useMemo(() => {
    const map = new Map<string, PresenceSince>();
    for (const event of presence) {
      const previous = map.get(event.device_id);
      if (previous && previous.time > event.time) {
        continue;
      }
      map.set(event.device_id, { state: event.state, time: event.time });
    }
    return map;
  }, [presence]);
}
