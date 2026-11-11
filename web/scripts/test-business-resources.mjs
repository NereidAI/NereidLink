// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression coverage for gateway-derived business resource selectors. These
// pure tests prove multi-gateway intersection and stale-reference clearing
// without mounting React Flow or depending on a live tenant inventory.

import assert from "node:assert/strict";
import {
  availableCompanionIds,
  businessTargetGatewayIds,
  clearChangedGatewayAssets,
  intersectReadyGatewayModels,
} from "../src/components/business/businessModel.ts";

const node = (id, kind, asset = {}) => ({
  id,
  kind,
  name: id,
  ref: "",
  asset,
  position: { x: 0, y: 0 },
});
const edge = (id, source, target, relation) => ({ id, source, target, relation });

const assembly = {
  nodes: [
    node("camera-a", "camera", { gateway_id: "gateway-a", device_id: "camera-a" }),
    node("camera-b", "camera", { gateway_id: "gateway-b", device_id: "camera-b" }),
    node("camera-other", "camera", { gateway_id: "gateway-other" }),
    node("pipeline", "pipeline", { pipeline_id: "pipeline-live" }),
    node("other-pipeline", "pipeline", { pipeline_id: "pipeline-other" }),
    node("model", "model", {
      model_id: "model-old",
      version: "v1",
      precision: "fp16",
    }),
    node("companion", "companion", {
      companion_id: "face-service",
      conclusion_device_id: "conclusion-1",
    }),
  ],
  edges: [
    edge("frames-a", "camera-a", "pipeline", "frames"),
    edge("frames-b", "camera-b", "pipeline", "frames"),
    edge("frames-other", "camera-other", "other-pipeline", "frames"),
    edge("uses", "model", "pipeline", "uses"),
    edge("facts", "pipeline", "companion", "facts"),
  ],
};

assert.deepEqual(
  businessTargetGatewayIds(assembly, "model"),
  ["gateway-a", "gateway-b"],
  "a model inherits every camera gateway feeding its related pipeline",
);
assert.deepEqual(
  businessTargetGatewayIds(assembly, "companion"),
  ["gateway-a", "gateway-b"],
  "a companion inherits targets through incoming pipeline facts",
);
assert.deepEqual(
  businessTargetGatewayIds(assembly, "pipeline"),
  [],
  "non-model/companion nodes do not acquire a synthetic resource scope",
);

const ready = (id, version, overrides = {}) => ({
  id,
  version,
  state: "READY",
  stale: false,
  ...overrides,
});
const commonModels = intersectReadyGatewayModels([
  [
    ready("shared", "v1", { model_name: "Shared model" }),
    ready("shared", "v1"),
    ready("shared", "v2", { stale: true }),
    ready("failed", "v1", { state: "FAILED" }),
  ],
  [ready("shared", "v1"), ready("shared", "v2"), ready("only-b", "v1")],
]);
assert.deepEqual(
  commonModels.map((model) => [model.id, model.version]),
  [["shared", "v1"]],
  "only fresh READY id/version identities present on every gateway survive",
);
assert.deepEqual(intersectReadyGatewayModels([]), [], "no target inventory means no choice");
assert.deepEqual(
  intersectReadyGatewayModels([[]]),
  [],
  "an empty target inventory cannot be bypassed",
);

const endpoints = [
  { companion_id: "face-service", gateway_id: "gateway-a", enabled: true },
  { companion_id: "face-service", gateway_id: "gateway-b", enabled: true },
  { companion_id: "meter-service", gateway_id: "gateway-a", enabled: true },
  { companion_id: "meter-service", gateway_id: "gateway-b", enabled: false },
  { companion_id: "disabled-service", gateway_id: "gateway-a", enabled: false },
  { companion_id: "disabled-service", gateway_id: "gateway-b", enabled: false },
];
assert.deepEqual(
  availableCompanionIds(endpoints, ["gateway-b", "gateway-a"]),
  ["face-service"],
  "a companion needs an enabled endpoint on every target gateway",
);
assert.deepEqual(availableCompanionIds(endpoints, []), []);

const changedGateway = structuredClone(assembly);
changedGateway.nodes.find((item) => item.id === "camera-a").asset.gateway_id = "gateway-c";
const reconciled = clearChangedGatewayAssets(assembly, changedGateway);
assert.notEqual(reconciled, changedGateway, "a real target change produces a reconciled assembly");
assert.deepEqual(
  reconciled.nodes.find((item) => item.id === "model").asset,
  { precision: "fp16" },
  "changing target clears model id/version but preserves independent settings",
);
assert.deepEqual(
  reconciled.nodes.find((item) => item.id === "companion").asset,
  { conclusion_device_id: "conclusion-1" },
  "changing target clears only the gateway-scoped companion registration",
);

const clearedGateway = structuredClone(assembly);
delete clearedGateway.nodes.find((item) => item.id === "camera-a").asset.gateway_id;
const cleared = clearChangedGatewayAssets(assembly, clearedGateway);
assert.deepEqual(
  cleared.nodes.find((item) => item.id === "model").asset,
  { precision: "fp16" },
  "clearing a target gateway also clears the dependent model identity",
);
assert.deepEqual(
  cleared.nodes.find((item) => item.id === "companion").asset,
  { conclusion_device_id: "conclusion-1" },
  "clearing a target gateway also clears the dependent companion identity",
);

const editedModel = structuredClone(assembly);
editedModel.nodes.find((item) => item.id === "model").asset.model_id = "model-new";
assert.equal(
  clearChangedGatewayAssets(assembly, editedModel),
  editedModel,
  "editing the model itself must not be mistaken for a target-context change",
);
assert.equal(
  editedModel.nodes.find((item) => item.id === "model").asset.model_id,
  "model-new",
);

console.log("Business resource selector regression tests passed.");
