// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useDeviceShadow owns the shared REST-plus-WebSocket device-shadow view.
// Every consumer receives the same fresh complete document without polling;
// local time-based health checks must never evaluate an abandoned REST cache.

import { useEffect, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { getShadow } from "../../api/shadow";
import { useWSStore } from "../../store/wsStore";
import { reduceDeviceShadowEvents } from "./liveShadow";

// useDeviceShadow fetches the initial shadow and keeps its React Query cache
// current through one device-scoped WebSocket subscription per consumer.
export function useDeviceShadow(deviceId: string, subscriptionOwner: string) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["device-shadow", deviceId],
    queryFn: () => getShadow(deviceId),
  });
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const shadowEvents = useWSStore((state) => state.shadowEvents);
  const cursorRef = useRef<number | null>(null);
  const subscriptionId = `${subscriptionOwner}:${deviceId}`;

  useEffect(() => {
    cursorRef.current = useWSStore.getState().shadowEvents.length;
    subscribe(subscriptionId, "shadow", [deviceId]);
    return () => unsubscribe(subscriptionId);
  }, [deviceId, subscribe, subscriptionId, unsubscribe]);

  useEffect(() => {
    if (cursorRef.current === null) return;
    const next = reduceDeviceShadowEvents(
      shadowEvents,
      cursorRef.current,
      deviceId,
    );
    cursorRef.current = next.cursor;
    if (next.shadow) {
      queryClient.setQueryData(["device-shadow", deviceId], next.shadow);
    }
  }, [deviceId, queryClient, shadowEvents]);

  return query;
}
