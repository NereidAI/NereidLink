// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useOnlineMap derives the latest ONLINE/OFFLINE state per device from the
// presence rolling buffer. The buffer holds events since page load, so an
// absent device is reported as UNKNOWN rather than assumed offline.

import { useMemo } from "react";

import { useWSStore } from "../store/wsStore";

// useOnlineMap returns deviceID -> latest presence state.
export function useOnlineMap(): Map<string, string> {
  const presence = useWSStore((state) => state.presence);

  return useMemo(() => {
    const map = new Map<string, string>();
    for (const event of presence) {
      map.set(event.device_id, event.state);
    }
    return map;
  }, [presence]);
}
