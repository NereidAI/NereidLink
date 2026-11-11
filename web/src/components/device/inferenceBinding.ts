// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// inferenceBinding.ts owns the edge-inference connector write decision and
// site-local acquisition options for the model binding wizard. Keeping them
// separate from the TSX surface makes the actual wire config regression-testable.

import type { ConnectorSaveInput, ConnectorSpec } from "../../api/connectors";

export const EDGE_INFERENCE_TYPE = "edge-inference";

// INFERENCE_TARGET_DEVICE_OPTIONS offers common OpenVINO target strings while
// AutoComplete still permits custom GPU.2 and MULTI/HETERO composites. The
// edge owns availability validation and must never silently fall back to CPU.
export const INFERENCE_TARGET_DEVICE_OPTIONS = [
  "CPU",
  "GPU",
  "GPU.0",
  "GPU.1",
  "GPU.2",
  "NPU",
  "NPU.0",
  "AUTO",
  "MULTI:GPU,CPU",
  "HETERO:GPU,CPU",
].map((value) => ({ value }));

// InferenceModelIdentity is the model inventory identity selectable in the
// binding wizard. Versions are distinct deployable variants of one model id.
export interface InferenceModelIdentity {
  id: string;
  version: string;
}

// VAAPI_DEVICE_PATTERN permits only a DRM render node, with or without its
// fixed /dev/dri prefix. It never accepts an arbitrary device path.
export const VAAPI_DEVICE_PATTERN = /^(?:\/dev\/dri\/)?renderD\d+$/;

// InferenceDecodeOptions describes the requested site-local ffmpeg decoder.
export interface InferenceDecodeOptions {
  supported: boolean;
  rtspSource: boolean;
  // undefined mirrors the software default when the conditional form item was
  // never mounted: the wizard uses preserve={false}, so an unmounted field
  // clears its value instead of retaining the initial "software" string.
  decodeAccel: "software" | "vaapi" | undefined;
  vaapiDevice: string;
}

// InferenceDecodeConfigResult either carries opaque connector config keys or
// identifies the localized validation error the wizard must present.
export type InferenceDecodeConfigResult =
  | { config: Record<string, string> }
  | { error: "decodeAccelUnsupported" | "decodeAccelSourceInvalid" | "vaapiDeviceInvalid" };

// planInferenceDecodeConfig keeps software bindings byte-compatible and emits
// the VA-API device only when explicitly selected. The default device belongs
// to NI, so an empty selection must remain absent rather than becoming a path.
export function planInferenceDecodeConfig(
  options: InferenceDecodeOptions,
): InferenceDecodeConfigResult {
  // Only an explicit VA-API request is gated. Any other value — "software",
  // undefined from a conditionally unmounted form item (preserve={false}),
  // or a future empty spelling — stays on the software default. The previous
  // strict "software" equality turned undefined into a misleading
  // "firmware unsupported" error on stale capability data (see L-151).
  if (options.decodeAccel !== "vaapi") return { config: {} };
  if (!options.supported) return { error: "decodeAccelUnsupported" };
  if (!options.rtspSource) return { error: "decodeAccelSourceInvalid" };
  if (options.vaapiDevice && !VAAPI_DEVICE_PATTERN.test(options.vaapiDevice)) {
    return { error: "vaapiDeviceInvalid" };
  }
  return {
    config: {
      decode_accel: "vaapi",
      ...(options.vaapiDevice
        ? { vaapi_device: options.vaapiDevice }
        : {}),
    },
  };
}

// shortHash renders djb2 as eight hex characters for stable, bounded names.
function shortHash(input: string): string {
  let h = 5381;
  for (let i = 0; i < input.length; i++) {
    h = ((h << 5) + h + input.charCodeAt(i)) >>> 0;
  }
  return h.toString(16).padStart(8, "0");
}

// directSourceSlug sanitizes the host and disambiguates paths on that host.
function directSourceSlug(raw: string): string {
  const value = raw.trim();
  let host = "";
  try {
    host = new URL(value).host;
  } catch {
    host = value;
  }
  const safe = host.replace(/[^A-Za-z0-9_.:,-]+/g, "-").slice(0, 64);
  return `${safe}-${shortHash(value)}`;
}

// inferenceOutputDeviceName builds the deterministic virtual output device
// name for a managed source or a direct URL and the selected model variant.
// Inventory rows may share one model id (for example INT8 and FP16 exports),
// so the version must participate in the identity to permit parallel bindings.
// ONVIF profiles likewise identify different streams on one camera. The
// profile suffix is omitted for camera-default sources to retain their names.
export function inferenceOutputDeviceName(
  source: {
    directUrl?: string;
    sourceCanonical?: string;
    sourceProfile?: string;
  },
  model: InferenceModelIdentity,
): string {
  const modelVariant = `${model.id}:${shortHash(model.version)}`;
  if (source.directUrl !== undefined) {
    return `inference:url:${directSourceSlug(source.directUrl)}:${modelVariant}`;
  }
  const profile = source.sourceProfile?.trim() ?? "";
  const sourceIdentity = profile
    ? `${source.sourceCanonical}:profile-${shortHash(profile)}`
    : source.sourceCanonical;
  return `inference:${sourceIdentity}:${modelVariant}`;
}

// EdgeInferenceBindingWrite describes the desired-spec write without hiding
// whether this is a new binding or a new revision of an existing one.
export type EdgeInferenceBindingWrite =
  | { kind: "create"; input: ConnectorSaveInput }
  | { kind: "update"; specId: string; input: ConnectorSaveInput };

// asRecord reads nested connector config without trusting its wire shape.
function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object"
    ? (value as Record<string, unknown>)
    : {};
}

// findEdgeInferenceBinding locates the existing desired spec for exactly the
// same source and model id+version. A managed source includes its ONVIF media
// profile because main/sub streams are independent acquisitions. Revision is
// deliberately excluded: a new build revision of the same version replaces
// that binding. Matching semantic identity as well as the new versioned device
// name keeps pre-versioning and pre-profile-aware output devices reusable
// instead of creating a duplicate after upgrade.
export function findEdgeInferenceBinding(
  config: Record<string, unknown>,
  bindings: ConnectorSpec[],
): ConnectorSpec | undefined {
  const source = asRecord(config.source);
  const model = asRecord(config.model_ref);
  return bindings.find((binding) => {
    if (binding.connector_type !== EDGE_INFERENCE_TYPE) return false;
    const otherSource = asRecord(binding.config?.source);
    const otherModel = asRecord(binding.config?.model_ref);
    if (source.device !== undefined || otherSource.device !== undefined) {
      if (source.device !== otherSource.device) return false;
      const profile =
        typeof source.profile === "string" ? source.profile.trim() : "";
      const otherProfile =
        typeof otherSource.profile === "string"
          ? otherSource.profile.trim()
          : "";
      if (profile !== otherProfile) return false;
    } else {
      const url = typeof source.url === "string" ? source.url.trim() : "";
      const otherUrl =
        typeof otherSource.url === "string" ? otherSource.url.trim() : "";
      if (!url || url !== otherUrl) return false;
    }
    return (
      typeof model.id === "string" &&
      model.id === otherModel.id &&
      typeof model.version === "string" &&
      model.version === otherModel.version
    );
  });
}

// planEdgeInferenceBindingWrite builds the connector write for one virtual
// output device. The database permits one connector per device, so an exact
// source+model-version rebind bumps the existing spec's revision and retains
// its device. Another profile or version never updates an occupied device.
export function planEdgeInferenceBindingWrite(
  deviceId: string,
  config: Record<string, unknown>,
  bindings: ConnectorSpec[],
): EdgeInferenceBindingWrite {
  const input: ConnectorSaveInput = {
    connector_type: EDGE_INFERENCE_TYPE,
    type_version: "1",
    device_id: deviceId,
    config,
  };
  const existing = findEdgeInferenceBinding(config, bindings);
  return existing
    ? {
        kind: "update",
        specId: existing.spec_id,
        input: { ...input, device_id: existing.device_id ?? deviceId },
      }
    : { kind: "create", input };
}
