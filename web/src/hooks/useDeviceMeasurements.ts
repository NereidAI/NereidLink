// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useDeviceMeasurements is the single source of the latest measurement
// set of one device. It discovers stored keys, subscribes to live
// telemetry under the caller-chosen subscription id and merges the two,
// so the latest-telemetry strip, the all-measurements table and any
// future widget share exactly one merge implementation.

import { useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import type { TelemetryKey } from "../api/devices";
import { listTelemetryKeys } from "../api/devices";
import { useWSStore, type TelemetryPoint } from "../store/wsStore";

// inferValueType classifies a live point whose key is not yet discovered.
function inferValueType(point: TelemetryPoint): TelemetryKey["value_type"] {
  if (point.value_type === "boolean" || point.value_type === "string") {
    return point.value_type;
  }
  if (typeof point.v_bool === "boolean") {
    return "boolean";
  }
  if (point.v_string) {
    return "string";
  }
  return "number";
}

// UseDeviceMeasurementsResult exposes the merged, newest-first key list
// and the backing discovery query.
interface UseDeviceMeasurementsResult {
  // Keys is every measurement, most recently updated first.
  keys: TelemetryKey[];

  // Loading reflects key discovery.
  loading: boolean;
}

// useDeviceMeasurements returns the merged measurements of a device.
// subscriptionId must be unique per caller so two widgets do not replace
// each other's server-side subscription.
export function useDeviceMeasurements(
  deviceId: string,
  subscriptionId: string,
): UseDeviceMeasurementsResult {
  const livePoints = useWSStore((state) => state.points);
  const subscribe = useWSStore((state) => state.subscribe);

  const keysQuery = useQuery({
    queryKey: ["device-telemetry-keys", deviceId],
    queryFn: () => listTelemetryKeys(deviceId),
  });

  // Each widget owns its subscription; the strip stays subscribed even
  // when the chart tab is unmounted.
  useEffect(() => {
    subscribe(subscriptionId, "telemetry", [deviceId]);
  }, [subscribe, subscriptionId, deviceId]);

  const keys = useMemo(() => {
    const map = new Map<string, TelemetryKey>();
    for (const key of keysQuery.data ?? []) {
      map.set(key.measurement_key, { ...key });
    }
    for (const point of livePoints as TelemetryPoint[]) {
      if (point.device_id !== deviceId) {
        continue;
      }
      const previous = map.get(point.measurement_key);
      // Live frames may arrive slightly out of order or duplicate a
      // stored row; never let an older frame overwrite a newer value.
      if (previous && previous.time > point.time) {
        continue;
      }
      map.set(point.measurement_key, {
        measurement_key: point.measurement_key,
        value_type: inferValueType(point),
        unit: point.unit ?? previous?.unit,
        time: point.time,
        v_double: point.v_double,
        v_bool: point.v_bool,
        v_string: point.v_string,
        quality: point.quality ?? previous?.quality,
      });
    }
    return [...map.values()].sort((a, b) => b.time.localeCompare(a.time));
  }, [keysQuery.data, livePoints, deviceId]);

  return { keys, loading: keysQuery.isLoading };
}
