// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// usePinnedTelemetry stores the operator-pinned measurement keys of
// one device as a browser preference backed by a shared zustand store.
// The table (where toggling happens) and the strip subscribe to one
// store, so a pin change updates both immediately. Pinning is local UI
// state; a future device-profile key_point flag would take precedence
// on the server side.

import { useEffect } from "react";
import { create } from "zustand";
import { useStore } from "zustand";

// storagePrefix namespaces pinned keys in localStorage.
const storagePrefix = "nl:pinned-telemetry:";

// PinnedState maps a device ID onto its stored keys. Undefined means
// the operator never customized pins, so callers can fall back to the
// most recently updated keys; an explicit empty array means "none".
interface PinnedState {
  // devices stores one preference per device ID.
  devices: Record<string, string[] | undefined>;

  // togglePin adds or removes one measurement key for a device.
  togglePin: (deviceId: string, measurementKey: string) => void;
}

// storageKey builds the per-device preference key.
function storageKey(deviceId: string): string {
  return storagePrefix + deviceId;
}

// readPinned returns undefined when the operator never customized pins.
function readPinned(deviceId: string): string[] | undefined {
  try {
    const raw = window.localStorage.getItem(storageKey(deviceId));
    if (raw === null) {
      return undefined;
    }
    const parsed = JSON.parse(raw) as unknown;
    return Array.isArray(parsed) ? (parsed as string[]) : [];
  } catch {
    return undefined;
  }
}

// persistPinned writes one preference; failures leave in-memory state
// usable for the current page.
function persistPinned(deviceId: string, keys: string[]) {
  try {
    window.localStorage.setItem(storageKey(deviceId), JSON.stringify(keys));
  } catch {
    // Storage may be unavailable (private mode).
  }
}

// pinnedStore is module-level shared state. Entries load lazily on first
// subscription to avoid touching every device preference at startup.
const pinnedStore = create<PinnedState>((set) => ({
  devices: {},
  togglePin: (deviceId, measurementKey) =>
    set((state) => {
      const current = state.devices[deviceId] ?? readPinned(deviceId) ?? [];
      const next = current.includes(measurementKey)
        ? current.filter((key) => key !== measurementKey)
        : [...current, measurementKey];
      persistPinned(deviceId, next);
      return { devices: { ...state.devices, [deviceId]: next } };
    }),
}));

// UsePinnedTelemetryResult exposes the pinned keys and pin toggling.
interface UsePinnedTelemetryResult {
  // Pinned is stored keys in pinned order; undefined means default.
  pinned: string[] | undefined;

  // togglePin adds or removes one measurement key.
  togglePin: (measurementKey: string) => void;

  // isPinned reports whether a key is currently pinned.
  isPinned: (measurementKey: string) => boolean;
}

// usePinnedTelemetry manages the per-device pinned-key preference.
export function usePinnedTelemetry(
  deviceId: string,
): UsePinnedTelemetryResult {
  // ensureLoaded pushes the persisted preference into the store once
  // per device; doing it in an effect keeps the zustand selector pure.
  useEffect(() => {
    if (deviceId in pinnedStore.getState().devices) {
      return;
    }
    pinnedStore.setState((state) => ({
      devices: { ...state.devices, [deviceId]: readPinned(deviceId) },
    }));
  }, [deviceId]);

  const devices = useStore(pinnedStore, (state) => state.devices);
  const pinned = devices[deviceId];

  return {
    pinned,
    togglePin: (measurementKey: string) =>
      pinnedStore.getState().togglePin(deviceId, measurementKey),
    isPinned: (measurementKey: string) =>
      pinned?.includes(measurementKey) ?? false,
  };
}
