// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression loop for direct-URL inference identity and writes. Exact model
// variants update their retained binding, while parallel variants never share
// an output device or overwrite one another.

import assert from "node:assert/strict";
import {
  inferenceOutputDeviceName,
  planEdgeInferenceBindingWrite,
  planInferenceDecodeConfig,
} from "../src/components/device/inferenceBinding.ts";

// Production regression: two READY inventory rows share one model id and are
// differentiated only by version. They must not share an output device.
const productionUrl = "rtsp://192.0.2.108/stream2";
const int8Name = inferenceOutputDeviceName(
  { directUrl: productionUrl },
  { id: "m-1740a77bae2d-49271", version: "job-aa46d7b79471-28939-ir-int8" },
);
const fp16Name = inferenceOutputDeviceName(
  { directUrl: productionUrl },
  { id: "m-1740a77bae2d-49271", version: "job-aa46d7b79471-28939-ir-fp16" },
);
assert.notEqual(
  int8Name,
  fp16Name,
  "INT8 and FP16 variants on one RTSP stream require different output devices",
);

const int8Config = {
  source: { mode: "url", transport: "rtsp", url: productionUrl },
  model_ref: {
    id: "m-1740a77bae2d-49271",
    version: "job-aa46d7b79471-28939-ir-int8",
    revision: "13828",
  },
};
const fp16Config = {
  source: { mode: "url", transport: "rtsp", url: productionUrl },
  model_ref: {
    id: "m-1740a77bae2d-49271",
    version: "job-aa46d7b79471-28939-ir-fp16",
    revision: "26016",
  },
};
const legacyInt8Binding = {
  spec_id: "spec-int8-legacy",
  connector_type: "edge-inference",
  device_id: "legacy-unversioned-int8-child",
  config: int8Config,
};
const legacyRebind = planEdgeInferenceBindingWrite(
  "new-versioned-int8-child",
  {
    ...int8Config,
    model_ref: { ...int8Config.model_ref, revision: "99999" },
  },
  [legacyInt8Binding],
);
assert.equal(
  legacyRebind.kind,
  "update",
  "an existing unversioned INT8 binding must remain reusable after upgrade",
);
assert.equal(legacyRebind.specId, "spec-int8-legacy");
assert.equal(
  legacyRebind.input.device_id,
  "legacy-unversioned-int8-child",
  "an exact rebind must retain the legacy device and ignore only the build revision",
);
const secondVariant = planEdgeInferenceBindingWrite(
  "new-versioned-fp16-child",
  fp16Config,
  [legacyInt8Binding],
);
assert.equal(
  secondVariant.kind,
  "create",
  "FP16 must create alongside INT8 instead of updating the INT8 spec",
);
const occupiedDevice = planEdgeInferenceBindingWrite(
  "legacy-unversioned-int8-child",
  fp16Config,
  [legacyInt8Binding],
);
assert.equal(
  occupiedDevice.kind,
  "create",
  "a different version must never overwrite INT8 even if given its device id",
);

// Managed ONVIF sources include the selected media profile in stream identity.
// The same model version on another profile is another stream, while another
// model version on the exact same profile must also remain independent.
const managedSource = {
  device: "523673dc-9cf2-4555-83c1-58ae9d5ec9fd",
  resource: "snapshot_jpeg",
  mode: "stream",
  profile: "Profile_1",
};
const managedInt8Config = { source: managedSource, model_ref: int8Config.model_ref };
const managedFp16Config = { source: managedSource, model_ref: fp16Config.model_ref };
const managedInt8Name = inferenceOutputDeviceName(
  { sourceCanonical: "dev-r5o9b3f4", sourceProfile: "Profile_1" },
  managedInt8Config.model_ref,
);
const managedFp16Name = inferenceOutputDeviceName(
  { sourceCanonical: "dev-r5o9b3f4", sourceProfile: "Profile_1" },
  managedFp16Config.model_ref,
);
const otherProfileInt8Name = inferenceOutputDeviceName(
  { sourceCanonical: "dev-r5o9b3f4", sourceProfile: "Profile_2" },
  managedInt8Config.model_ref,
);
assert.notEqual(
  managedInt8Name,
  managedFp16Name,
  "INT8 and FP16 on one managed camera profile need distinct output devices",
);
assert.notEqual(
  managedInt8Name,
  otherProfileInt8Name,
  "one model version on two ONVIF profiles needs distinct output devices",
);
assert.equal(
  inferenceOutputDeviceName(
    { sourceCanonical: "dev-r5o9b3f4" },
    managedInt8Config.model_ref,
  ),
  "inference:dev-r5o9b3f4:m-1740a77bae2d-49271:9df724b6",
  "default-profile output names must remain compatible with existing devices",
);
const managedInt8Binding = {
  spec_id: "spec-managed-int8-profile-1",
  connector_type: "edge-inference",
  device_id: "legacy-managed-int8-profile-1-child",
  config: managedInt8Config,
};
const managedFp16Write = planEdgeInferenceBindingWrite(
  "new-managed-fp16-profile-1-child",
  managedFp16Config,
  [managedInt8Binding],
);
assert.equal(
  managedFp16Write.kind,
  "create",
  "FP16 must create alongside INT8 on the exact same managed camera profile",
);
const otherProfileInt8Write = planEdgeInferenceBindingWrite(
  "new-managed-int8-profile-2-child",
  {
    ...managedInt8Config,
    source: { ...managedSource, profile: "Profile_2" },
  },
  [managedInt8Binding],
);
assert.equal(
  otherProfileInt8Write.kind,
  "create",
  "changing only the ONVIF profile must create another inference stream",
);
const exactManagedRebind = planEdgeInferenceBindingWrite(
  "new-profile-aware-managed-int8-child",
  {
    ...managedInt8Config,
    model_ref: { ...managedInt8Config.model_ref, revision: "99999" },
  },
  [managedInt8Binding],
);
assert.equal(
  exactManagedRebind.kind,
  "update",
  "an exact managed source/profile/version rebind remains an update",
);
assert.equal(
  exactManagedRebind.input.device_id,
  "legacy-managed-int8-profile-1-child",
  "an exact rebind retains the pre-profile-aware output device and alias owner",
);

const config = {
  source: {
    mode: "url",
    transport: "rtsp",
    url: "rtsp://camera.example/Streaming/Channels/101",
  },
  model_ref: { id: "model-a", version: "2" },
};

const existing = planEdgeInferenceBindingWrite("child-rtsp-model-a", config, [
  {
    spec_id: "spec-existing",
    connector_type: "edge-inference",
    device_id: "child-rtsp-model-a",
    config,
  },
]);
assert.equal(
  existing.kind,
  "update",
  "rebinding the same RTSP URL and model must update the existing connector instead of returning 409",
);
assert.equal(existing.specId, "spec-existing");

const fresh = planEdgeInferenceBindingWrite(
  "child-rtsp-model-b",
  { ...config, model_ref: { id: "model-b", version: "2" } },
  [
    {
      spec_id: "spec-existing",
      connector_type: "edge-inference",
      device_id: "child-rtsp-model-a",
      config,
    },
  ],
);
assert.equal(
  fresh.kind,
  "create",
  "another model on the same RTSP URL has its own output device and still creates a binding",
);

// Site-local decode acceleration is opaque connector config, never model
// metadata. Software remains absent for byte compatibility; VA-API preserves
// the selected render-node spelling for NE's explicit NI property mapping.
assert.deepEqual(
  planInferenceDecodeConfig({
    supported: false,
    rtspSource: false,
    decodeAccel: "software",
    vaapiDevice: "renderD129",
  }),
  { config: {} },
  "software bindings must omit both decode keys, including stale hidden form values",
);
// A conditionally unmounted form item (the wizard uses preserve={false})
// yields undefined instead of the initial "software" string. It must behave
// exactly like software even on stale capability data (supported=false),
// rather than raising a misleading firmware-unsupported error (L-151).
for (const supported of [false, true]) {
  assert.deepEqual(
    planInferenceDecodeConfig({
      supported,
      rtspSource: false,
      decodeAccel: undefined,
      vaapiDevice: "renderD129",
    }),
    { config: {} },
    `undefined decode selection (supported=${supported}) must default to software, not error`,
  );
}
assert.deepEqual(
  planInferenceDecodeConfig({
    supported: true,
    rtspSource: true,
    decodeAccel: "vaapi",
    vaapiDevice: "",
  }),
  { config: { decode_accel: "vaapi" } },
  "the default VA-API device must remain absent so NI selects renderD128",
);
for (const device of ["renderD129", "/dev/dri/renderD130"]) {
  assert.deepEqual(
    planInferenceDecodeConfig({
      supported: true,
      rtspSource: true,
      decodeAccel: "vaapi",
      vaapiDevice: device,
    }),
    { config: { decode_accel: "vaapi", vaapi_device: device } },
  );
}
assert.deepEqual(
  planInferenceDecodeConfig({
    supported: false,
    rtspSource: true,
    decodeAccel: "vaapi",
    vaapiDevice: "",
  }),
  { error: "decodeAccelUnsupported" },
);
for (const [device, expected] of [
  ["../dri/x", "vaapiDeviceInvalid"],
  ["renderD129 ", "vaapiDeviceInvalid"],
  ["renderd129", "vaapiDeviceInvalid"],
  ["/dev/dri/renderD129/other", "vaapiDeviceInvalid"],
  ["renderD129", "decodeAccelSourceInvalid"],
]) {
  assert.deepEqual(
    planInferenceDecodeConfig({
      supported: true,
      rtspSource: expected !== "decodeAccelSourceInvalid",
      decodeAccel: "vaapi",
      vaapiDevice: device,
    }),
    { error: expected },
  );
}
const vaapiWrite = planEdgeInferenceBindingWrite(
  "child-vaapi",
  {
    ...config,
    ...planInferenceDecodeConfig({
      supported: true,
      rtspSource: true,
      decodeAccel: "vaapi",
      vaapiDevice: "renderD129",
    }).config,
  },
  [],
);
assert.equal(vaapiWrite.input.config.decode_accel, "vaapi");
assert.equal(vaapiWrite.input.config.vaapi_device, "renderD129");
assert.equal("ffmpeg_extra" in vaapiWrite.input.config, false);

console.log("inference binding regression checks passed");
