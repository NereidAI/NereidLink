// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// wsStore owns the single WebSocket connection and its resilience behavior
// (ADR-0020/ADR-0030):
//
//   - exactly one connection for the whole page, multiplexing many
//     subscriptions;
//   - automatic reconnect with capped exponential backoff;
//   - re-subscription replay after reconnect, followed by a resume request
//     carrying the last per-subscription sequence cursor;
//   - credential renewal through the reauth frame;
//   - rolling buffers for pushed resources shared by all components.
//
// Real-time state always arrives as server pushes; the store never polls.

import { create } from "zustand";
import i18next from "i18next";

import { listPresence } from "../api/presence";
import { currentTenantId } from "../api/tenant";
import type { BatchTask } from "../api/batchCommands";
import type { OtaTask, OtaItem, OtaTimelineEvent } from "../api/ota";
import type { Shadow } from "../api/shadow";
import { authStore } from "./authStore";

// ResourceType enumerates subscribable real-time resources.
export type ResourceType =
  | "telemetry"
  | "presence"
  | "alarm"
  | "notification"
  | "command_task"
  | "ota_task"
  | "probe_task"
  | "connector"
  | "model_inventory"
  | "secret_binding"
  | "attribute"
  | "shadow"
  | "member";

// TelemetryPoint is one normalized real-time value pushed by the server.
export interface TelemetryPoint {
  time: string;
  device_id: string;
  measurement_key: string;
  v_double: number;
  quality: string;
  raw_value?: number;
  unit?: string;
  descriptor_version?: number;
  v_string?: string;
  v_bool?: boolean;
  value_type?: string;
}

// PresenceEvent is one connection-state boundary pushed by the server.
export interface PresenceEvent {
  time: string;
  device_id: string;
  state: string;
  reason: string;
  session_id: string;
}

// AlarmEvent is one alarm lifecycle frame pushed by the server.
export interface AlarmEvent {
  alarm_id: string;
  tenant_id: string;
  device_id: string;
  alarm_type: string;
  severity: string;
  state: string;
  started_at: string;
  cleared_at?: string;
  details?: Record<string, unknown>;
}

// NotificationEvent is one in-app notification frame pushed by the server.
export interface NotificationEvent {
  notification_id: string;
  tenant_id: string;
  title: string;
  body?: string;
  severity?: string;
  created_at: string;
  read: boolean;
}

// BatchTaskEvent is one batch task progress frame.
export interface BatchTaskEvent {
  type: "task" | "item";
  task: BatchTask;
  item?: unknown;
}

// ProbeTask is the aggregate state of an on-demand probe task.
export interface ProbeTask {
  id: string;
  tenant_id: string;
  mode: string;
  timeout_ms: number;
  state: "pending" | "running" | "completed" | "partial" | "failed";
  total_count: number;
  succeeded_count: number;
  failed_count: number;
  error?: string;
  created_at: string;
  finished_at?: string;
}

// ProbeTaskEvent is one on-demand probe progress frame.
export interface ProbeTaskEvent {
  type: "task" | "item";
  task: ProbeTask;
  item?: unknown;
}

// OtaTaskEvent is one firmware rollout progress frame (aggregate, item
// latest state, or an append-only timeline event).
export interface OtaTaskEvent {
  type: "task" | "item" | "event";
  task: OtaTask;
  item?: OtaItem;
  event?: OtaTimelineEvent;
}

// ConnectorEvent is one edge connector lifecycle notification: a desired
// write/deletion, an edge-reported status, or a manifest reconciliation.
// The frame only signals that a gateway's view changed; consumers re-fetch
// the connector list/reconcile REST view rather than patching from it.
export interface ConnectorEvent {
  kind: "desired" | "deleted" | "status" | "manifest";
  tenant_id: string;
  gateway_id: string;
  spec_id?: string;
  state?: string;
  revision?: number;
}

// ModelInventoryEvent is one edge model-inventory change notification
// (ADR-0039): a models reconciliation cycle, a lease-stale transition, or a
// runtime-plane change. Consumers re-fetch the gateway's model list.
export interface ModelInventoryEvent {
  kind: "inventory" | "stale" | "runtime";
  tenant_id: string;
  gateway_id: string;
}

// SecretBindingEvent is one edge secret delivery notification: a desired
// write, a withdrawal, or an edge-reported status. The frame only signals a
// gateway view change; consumers re-fetch the binding REST list.
export interface SecretBindingEvent {
  kind: "desired" | "withdrawn" | "status";
  tenant_id: string;
  gateway_id: string;
  secret_name?: string;
  status?: string;
  revision?: number;
}

// MemberEvent is one tenant member administration change notification
// (account created, roles/data scope replaced, enable/disable, removal).
// The frame only signals that the tenant's member list changed; platform
// admins re-fetch the member REST list rather than patching from it.
export interface MemberEvent {
  kind: "created" | "roles" | "data_scope" | "enabled" | "disabled" | "removed";
  tenant_id: string;
  user_id: string;
  actor_id?: string;
  at?: string;
}

// AttributeEvent is one changed device attribute key pushed after a commit
// by either the platform REST write path (source "server") or a
// device/rule-engine write (source "device").
export interface AttributeEvent {
  tenant_id: string;
  device_id: string;
  scope: string;
  key: string;
  value: unknown;
  updated_at: string;
  source: "server" | "device";
}

// ShadowEvent is the complete post-commit device shadow pushed after a
// desired patch (source "server") or a device-reported update (source
// "device"). Consumers replace their whole shadow view from the frame.
export interface ShadowEvent extends Shadow {
  tenant_id: string;
  source: "server" | "device";
}

// subscription is one active client-side subscription to replay on reconnect.
interface subscription {
  id: string;
  type: ResourceType;
  devices: string[];
  // tenant is recorded explicitly so cross-tenant subscriptions (a platform
  // admin viewing another tenant's panel) replay against the same tenant the
  // subscription was opened for instead of the session tenant.
  tenant: string;
  lastSeq?: number;
}

// ServerFrame is the union of frames accepted from the server.
interface ServerFrame {
  type: string;
  id?: string;
  seq?: number;
  code?: string;
  message?: string;
  data?: unknown;
}

// buffer sizes keep long-running pages at flat memory.
const MAX_POINTS = 2000;
const MAX_ALARMS = 500;
const MAX_PRESENCE = 500;
const MAX_NOTIFICATIONS = 500;
const MAX_BATCH_TASKS = 200;
const MAX_PROBE_TASKS = 200;
const MAX_OTA_TASKS = 200;
const MAX_CONNECTOR_EVENTS = 200;
const MAX_MODEL_EVENTS = 200;
const MAX_SECRET_BINDING_EVENTS = 200;
const MAX_ATTRIBUTE_EVENTS = 500;
const MAX_SHADOW_EVENTS = 500;
const MAX_MEMBER_EVENTS = 200;

// reconnect tuning: capped exponential backoff keeps a storm of clients
// re-establishing in a staggered fashion instead of thundering the gateway.
const INITIAL_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 15000;

// WSActions defines the imperative surface used by components.
interface WSActions {
  connect: () => void;
  disconnect: () => void;
  subscribe: (
    id: string,
    type: ResourceType,
    devices: string[],
    tenant?: string,
  ) => void;
  unsubscribe: (id: string) => void;
  reauth: (token: string) => void;
}

// WSState is the complete store surface.
interface WSState extends WSActions {
  connected: boolean;
  points: TelemetryPoint[];
  presence: PresenceEvent[];
  alarms: AlarmEvent[];
  notifications: NotificationEvent[];
  batchTaskEvents: BatchTaskEvent[];
  probeTaskEvents: ProbeTaskEvent[];
  otaTaskEvents: OtaTaskEvent[];
  connectorEvents: ConnectorEvent[];
  modelInventoryEvents: ModelInventoryEvent[];
  secretBindingEvents: SecretBindingEvent[];
  attributeEvents: AttributeEvent[];
  shadowEvents: ShadowEvent[];
  memberEvents: MemberEvent[];
}

// module-level connection bookkeeping sits outside React state so the
// reconnect logic survives component unmount cycles.
let socket: WebSocket | null = null;
let manualClose = false;
let backoff = INITIAL_BACKOFF_MS;
let activeSubscriptions: subscription[] = [];

// findSubscription returns the tracked subscription by client-local id.
function findSubscription(id: string): subscription | undefined {
  return activeSubscriptions.find((subscription) => subscription.id === id);
}

// append keeps the tail of a rolling buffer.
function append<T>(buffer: T[], value: T, max: number): T[] {
  return [...buffer, value].slice(-max);
}

export const useWSStore = create<WSState>((set) => ({
  connected: false,
  points: [],
  presence: [],
  alarms: [],
  notifications: [],
  batchTaskEvents: [],
  probeTaskEvents: [],
  otaTaskEvents: [],
  connectorEvents: [],
  modelInventoryEvents: [],
  secretBindingEvents: [],
  attributeEvents: [],
  shadowEvents: [],
  memberEvents: [],

  connect: () => {
    if (socket && (socket.readyState === WebSocket.OPEN ||
      socket.readyState === WebSocket.CONNECTING)) {
      return;
    }
    manualClose = false;
    openSocket(set);
  },

  disconnect: () => {
    manualClose = true;
    socket?.close();
    socket = null;
    set({ connected: false });
  },

  subscribe: (id, type, devices, tenant = currentTenantId()) => {
    const existing = findSubscription(id);
    const recorded: subscription = {
      id,
      type,
      devices,
      tenant,
      lastSeq: existing?.lastSeq,
    };
    activeSubscriptions = [
      ...activeSubscriptions.filter((subscription) => subscription.id !== id),
      recorded,
    ];
    sendControl({
      op: "subscribe",
      id,
      // The tenant is mandatory on the server for multi-tenant principals and
      // keeps real-time frames from ever crossing tenant boundaries.
      resource: { type, tenant, devices },
    });
  },

  unsubscribe: (id) => {
    const existing = findSubscription(id);
    activeSubscriptions = activeSubscriptions.filter(
      (subscription) => subscription.id !== id,
    );
    if (existing) {
      sendControl({ op: "unsubscribe", id });
    }
  },

  reauth: (token) => {
    sendControl({ op: "reauth", token });
  },
}));

// openSocket creates the connection and wires lifecycle and message handling.
function openSocket(set: (partial: Partial<WSState>) => void) {
  // The browser WebSocket API cannot set an Authorization header, so
  // the bearer token travels as a query parameter and is verified
  // before the server upgrades.
  const token = authStore.getState().accessToken;
  const url = new URL(`${window.location.origin}/ws`);
  if (token) {
    url.searchParams.set("access_token", token);
  }
  socket = new WebSocket(url);

  socket.onopen = () => {
    backoff = INITIAL_BACKOFF_MS;
    set({ connected: true });
    replaySubscriptions();
    seedPresenceSnapshot(set);
  };

  socket.onmessage = (event: MessageEvent<string>) => {
    let frame: ServerFrame;
    try {
      frame = JSON.parse(event.data) as ServerFrame;
    } catch {
      return;
    }
    routeFrame(frame, set);
  };

  socket.onclose = () => {
    set({ connected: false });
    socket = null;
    if (manualClose) return;
    // Reconnect with capped exponential backoff.
    window.setTimeout(() => {
      if (!manualClose) openSocket(set);
    }, backoff);
    backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
  };
}

// seedPresenceSnapshot loads the current presence state so devices that
// connected before this client opened the socket are not stuck on UNKNOWN.
// Synthetic snapshot placeholders are merged into the rolling buffer instead
// of replacing it: real frames already buffered survive, and per-device
// placeholders are refreshed on reconnect without duplication. Consumers
// must treat reason === "SNAPSHOT" as baseline state, never as a boundary.
function seedPresenceSnapshot(set: (partial: Partial<WSState>) => void) {
  listPresence()
    .then((snapshot) => {
      const seedAt = new Date().toISOString();
      const seeded = new Map(
        snapshot.devices.map((device) => [device.device_id, device]),
      );
      const current = useWSStore.getState().presence;

      const realFrameAfterSeed = new Set<string>();
      for (const event of current) {
        if (event.time >= seedAt) realFrameAfterSeed.add(event.device_id);
      }

      const merged: PresenceEvent[] = [];
      const refreshed = new Set<string>();
      for (const event of current) {
        if (event.reason !== "SNAPSHOT" || !seeded.has(event.device_id)) {
          merged.push(event);
          continue;
        }
        // Replace an older placeholder for this device with this seed.
        if (refreshed.has(event.device_id)) continue;
        const device = seeded.get(event.device_id)!;
        refreshed.add(device.device_id);
        seeded.delete(device.device_id);
        merged.push({
          time: seedAt,
          device_id: device.device_id,
          state: "ONLINE",
          reason: "SNAPSHOT",
          session_id: device.session_id,
        });
      }
      for (const device of seeded.values()) {
        // A real boundary newer than this seed is already authoritative.
        if (realFrameAfterSeed.has(device.device_id)) continue;
        merged.push({
          time: seedAt,
          device_id: device.device_id,
          state: "ONLINE",
          reason: "SNAPSHOT",
          session_id: device.session_id,
        });
      }
      merged.sort((a, b) => a.time.localeCompare(b.time));
      set({ presence: merged.slice(-MAX_PRESENCE) });
    })
    .catch((error: unknown) => {
      console.warn("presence snapshot failed", error);
    });
}

// replaySubscriptions re-sends every subscription after a reconnect and,
// when a cursor exists, requests replay of any missed frames.
function replaySubscriptions() {
  for (const subscription of activeSubscriptions) {
    sendControl({
      op: "subscribe",
      id: subscription.id,
      resource: {
        type: subscription.type,
        tenant: subscription.tenant,
        devices: subscription.devices,
      },
    });
    if (subscription.lastSeq !== undefined) {
      sendControl({
        op: "resume",
        id: subscription.id,
        cursor: subscription.lastSeq,
      });
    }
  }
}

// routeFrame dispatches a pushed frame into the matching rolling buffer.
function routeFrame(
  frame: ServerFrame,
  set: (partial: Partial<WSState>) => void,
) {
  const state = useWSStore.getState();

  switch (frame.type) {
    case "telemetry": {
      const point = frame.data as TelemetryPoint;
      recordCursor(frame);
      set({ points: append(state.points, point, MAX_POINTS) });
      break;
    }
    case "presence": {
      const event = frame.data as PresenceEvent;
      recordCursor(frame);
      set({ presence: append(state.presence, event, MAX_PRESENCE) });
      break;
    }
    case "alarm": {
      const event = frame.data as AlarmEvent;
      recordCursor(frame);
      set({ alarms: append(state.alarms, event, MAX_ALARMS) });
      break;
    }
    case "notification": {
      const event = frame.data as NotificationEvent;
      recordCursor(frame);
      set({
        notifications: append(state.notifications, event, MAX_NOTIFICATIONS),
      });
      break;
    }
    case "command_task": {
      const event = frame.data as BatchTaskEvent;
      recordCursor(frame);
      set({
        batchTaskEvents: append(state.batchTaskEvents, event, MAX_BATCH_TASKS),
      });
      break;
    }
    case "probe_task": {
      const event = frame.data as ProbeTaskEvent;
      recordCursor(frame);
      set({
        probeTaskEvents: append(state.probeTaskEvents, event, MAX_PROBE_TASKS),
      });
      break;
    }
    case "ota_task": {
      const event = frame.data as OtaTaskEvent;
      recordCursor(frame);
      set({
        otaTaskEvents: append(state.otaTaskEvents, event, MAX_OTA_TASKS),
      });
      break;
    }
    case "connector": {
      const event = frame.data as ConnectorEvent;
      recordCursor(frame);
      set({
        connectorEvents: append(state.connectorEvents, event, MAX_CONNECTOR_EVENTS),
      });
      break;
    }
    case "model_inventory": {
      const event = frame.data as ModelInventoryEvent;
      recordCursor(frame);
      set({
        modelInventoryEvents: append(
          state.modelInventoryEvents,
          event,
          MAX_MODEL_EVENTS,
        ),
      });
      break;
    }
    case "secret_binding": {
      const event = frame.data as SecretBindingEvent;
      recordCursor(frame);
      set({
        secretBindingEvents: append(state.secretBindingEvents, event, MAX_SECRET_BINDING_EVENTS),
      });
      break;
    }
    case "attribute": {
      const event = frame.data as AttributeEvent;
      recordCursor(frame);
      set({
        attributeEvents: append(state.attributeEvents, event, MAX_ATTRIBUTE_EVENTS),
      });
      break;
    }
    case "shadow": {
      const event = frame.data as ShadowEvent;
      recordCursor(frame);
      set({
        shadowEvents: append(state.shadowEvents, event, MAX_SHADOW_EVENTS),
      });
      break;
    }
    case "member": {
      const event = frame.data as MemberEvent;
      recordCursor(frame);
      set({
        memberEvents: append(state.memberEvents, event, MAX_MEMBER_EVENTS),
      });
      break;
    }
    case "error":
      // Surface protocol errors through the active i18n language; non-blocking
      // and retained only in console for Stage 1 diagnostics.
      console.warn("ws error frame", frame.code, frame.message);
      break;
  }
}

// recordCursor updates the last sequence seen for the owning subscription so
// the next reconnect resumes from the correct position.
function recordCursor(frame: ServerFrame) {
  if (!frame.id || frame.seq === undefined) return;
  activeSubscriptions = activeSubscriptions.map((subscription) =>
    subscription.id === frame.id
      ? { ...subscription, lastSeq: frame.seq }
      : subscription,
  );
}

// sendControl writes one JSON control frame when the socket is open.
function sendControl(frame: Record<string, unknown>) {
  if (socket && socket.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify(frame));
  }
}

// currentLanguage is exported for future locale-aware reconnect flows.
export const currentLanguage = (): string => i18next.language;
