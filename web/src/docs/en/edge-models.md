<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Edge models

The **Edge models** tab is available on [gateway](help:glossary/gateway) devices whose firmware negotiated the `inference` feature during the hello handshake (ADR-0039). It shows the AI models physically present on that gateway and lets you bind a camera (sub-device) to a model through the generic `edge-inference` connector.

NereidLink never stores or transfers model bytes: it only keeps the inventory the gateway reports, displays it, and raises alarms when a model or the inference runtime is unhealthy.

## Three status planes

Model inference health is shown on three independent planes. A problem on one plane does not imply a problem on another.

1. **Inference runtime** — the edge inference serving process (for example a KServe/OpenAI-compatible runtime). The state is read from the gateway [device shadow](help:device-shadow) reported `inference` block: `ONLINE`, `DEGRADED`, `OFFLINE`, or **Unknown / stale** when no health report has arrived within three probe intervals. While this page remains open, complete shadow WebSocket events keep the cached health report current; a healthy runtime does not require a route change or manual refresh. The card also shows ready/total models, active bindings and the last reported latency.
2. **Model inventory** — the models the gateway last reported, each with its own lifecycle state.
3. **Bindings** — the `edge-inference` connector instances on this gateway and their reported connector state (`APPLIED`, `DEGRADED`, `FAILED`, …).

## Model inventory

The gateway reports its full model list in the manifest `models` segment. Every report is a **full replacement**: models missing from a new list are removed, and an explicit empty list means the gateway currently holds zero models. Older firmware that does not send the segment leaves the stored inventory untouched.

Each row shows:

- **Model** — the model id, semantic version, build revision and runtime model name.
- **Task / family** — the task family (e.g. `detection`) and the in-task result/post-processing family (e.g. `yolov8`).
- **Model server** — which server actually drives the model. Firmware that reports the concrete model-server product shows its family token (e.g. `ovms`, `triton`, `external`), with the inference protocol family (`kserve`, `openai`, `task-runtime`) underneath it; older firmware only knows the protocol family, which is then shown on its own. The protocol family is a wire protocol, never a vendor or instance name.
- **State** — one of:
  - `EXTRACTING` — the model package is being unpacked.
  - `VALIDATING` — the model is being checked.
  - `READY` — available for binding.
  - `FAILED` — the model failed; hover the tag to see the transport-safe reason code.
  - `REMOVED` — reported as removed on the edge.
- **Labels** — the model's class names, stored and shown verbatim. The platform never interprets or translates them.
- **Bindings** — how many `edge-inference` instances reference the model.

### Stale inventory

Inventory rows are marked **Stale** in two cases, and kept (not deleted) with a stale tag until the gateway reports the models segment again, which clears the marker:

1. the gateway stops sending manifests entirely (manifest lease expired), or
2. on a gateway that previously reported models, manifests keep arriving (other tabs stay live) but the `models` segment is omitted for more than 15 minutes — for example when the edge's inference service was cleared but the edge fails to send an authoritative empty `"models": []`.

A stale model cannot be selected for a new binding. The runtime card reading 0/0 while inventory rows linger without a stale tag is itself a sign the edge firmware should be checked.

## Bind a camera to a model

Use **Bind inference** to create a detection binding in one action. The button is enabled only when the gateway reports at least one `READY`, non-stale model.

The wizard:

1. Asks for the **source camera** (one of the gateway's managed sub-devices), an **acquisition mode** and a **READY model**.
2. Creates a virtual **output sub-device** named `inference:<source>[:profile-<profile-hash>]:<model-id>:<model-version-hash>` that references the built-in, tenant-visible `detection/v1` system profile. The profile segment appears when an ONVIF stream profile is explicitly selected; camera-default sources retain the older name without it. A managed binding's identity is camera + profile + model ID/version: one profile can run multiple model versions in parallel, and different profiles can run the same model version independently. An exact rebind reuses the retained output device, including devices named before profile-aware naming was introduced. The output telemetry points (`detection_count`, `top1_label`, `top1_score`, `infer_latency_ms`, `detections_json`) carry the inference results. For direct-URL bindings the name is derived deterministically from the URL and model version (`inference:url:<host>-<url-hash>:<model-id>:<model-version-hash>`), so one URL can likewise bind multiple model versions in parallel. The wizard suggests an editable **output sub-device alias** from the camera name (or direct-URL host) plus `model.id@version`. This alias is display metadata only and never affects binding identity; an exact rebind preserves the existing alias unless you edit it.
3. Creates an `edge-inference` connector on the gateway bound to that sub-device, with `model_ref` pointing at the chosen model id/version, the **target device** (`target_device`), the acquisition mode and the trigger settings. If the same source (including ONVIF profile) + model id+version is already bound, the wizard updates that connector to a new revision instead of attempting a duplicate create; another profile or model version creates a separate binding.

The **Inference bindings** table distinguishes parallel inference streams directly: every row shows the output-device alias and canonical name, the full bound model ID, a prominent full model-version tag, and the model build revision alongside acquisition, target device and connector state. Use this model column rather than relying on a custom alias to identify the binding; the state column shows whether that desired binding is applied, degraded or failed.

**Acquisition mode** decides how each frame is obtained:

- **Snapshot JPEG** (default): the edge calls the camera's ONVIF `Snapshot` command once per cycle and infers that single JPEG. Lightweight; misses anything between snapshots.
- **Video stream (RTSP)**: the edge resolves the RTSP URL locally from the camera's ONVIF registration (the URL and its credentials never leave the gateway, and are never stored in the cloud), keeps an ffmpeg subscription and infers the latest frame on every trigger tick. A **stream profile** picker appears for this mode: it enumerates the camera's ONVIF media profiles (main/sub/third stream with resolution, encoding and frame rate) through a read-only edge operation, and the empty entry means the camera's default (usually main) stream; a token may also be typed manually. A camera with no usable RTSP feed reports `source-no-frame` instead of silently falling back. Stream mode and profile selection appear only when the gateway's self-registered connector schema advertises them; older firmware hides them.
- **Direct URL (non-ONVIF)**: for cameras and encoders with no ONVIF registration at all — bind directly by address. Pick a **transport** (`RTSP over TCP`, `RTSP over UDP`, `Motion JPEG (HTTP)`, or `Single JPEG (HTTP)`) and enter the **camera URL** (`rtsp://host/...` for RTSP transports, `http(s)://host/...` for MJPEG/JPEG, at most 1024 characters). **Never put a username or password in the URL** — it is stored in the platform. If the camera requires authentication, an operator first creates a named secret (username/password) in the edge inference service's secret store and you pick its **edge secret name** from the filterable dropdown — it lists secrets delivered to this gateway (device **Secrets** tab) and a name provisioned directly on the edge can also be typed; the password never leaves the gateway. Prefer snapshot/stream when the camera supports ONVIF. A bad scheme, embedded credentials, or an unreachable/unauthorized feed is rejected (`config-invalid` at apply, `source-no-frame` at runtime), never silently switched to another acquisition.

**Trigger** controls inference frequency. **On event** infers on an on-demand read/diagnostic with a max-idle guard; **Periodic** infers every `period_ms`; **Continuous (FPS)** infers at a steady rate (0.1–60 FPS, default 5) — the typical pairing with video streams. The continuous option appears only on firmware whose schema enum includes it.

**Advanced options** (shown when the edge firmware declares them):

- **Attach evidence image** (`emit_frame`, default on): include the annotated evidence JPEG with every result event. Turning it off saves uplink bandwidth and storage, but result drill-down then has no image.
- **Annotate image / preview** (`annotate_preview`, default on): draw detection boxes and labels on the evidence image and on the local diagnostic preview. This is independent of attaching the image.
- **Custom ffmpeg arguments** (`ffmpeg_extra`, for video-stream and direct-URL acquisition): extra argv tokens for stream acquisition, space-separated with no shell or quotes. Only `-re` and `-r`/`-ss`/`-t`/`-threads`/`-fflags`/`-flags` (each with a value) are accepted — for example `-fflags nobuffer -flags low_delay`; the edge rejects anything else at apply time.
- **Hardware decoding** (`decode_accel`, ONVIF RTSP streams and direct `rtsp`/`rtsp-udp` only): software decoding is the default and sends no new key. **VA-API hardware decoding** sends `decode_accel=vaapi`; snapshot, MJPEG and JPEG sources do not offer it. A **GPU device** can select `renderD129`, `renderD130`, or a custom `renderD<N>`/`/dev/dri/renderD<N>` node (`vaapi_device`); the default omits it so NI uses `/dev/dri/renderD128`. These site-local pipeline settings do not enter model packages and are not expressed through `ffmpeg_extra`. The edge host must already provide the VA-API driver (for example `intel-media-driver`) and `/dev/dri` access (container device mapping or Kubernetes `gpu.intel.com/i915`). A missing device or driver fails the job explicitly; **there is no automatic software-decoding fallback**.

The **target device** is an operational deployment decision made at bind time, not part of the model package. Pick a preset (`CPU` (default), `GPU`, `GPU.0`/`GPU.1`/… for a specific accelerator index, `NPU`, `AUTO`) or type an OVMS composite form such as `MULTI:GPU,CPU` / `HETERO:GPU,CPU` (letters, digits and `_.:,-`, up to 64 characters). It is a hard requirement: the edge must produce results on that device and explicitly report a failure (`target-device-unavailable`) if it cannot — it never silently falls back to CPU. A binding created without this field behaves exactly as `CPU`. The target-device choice is enabled only when the gateway's edge firmware declares the property in its self-registered connector schema; older firmware shows the field locked to `CPU`.

If connector creation or update fails, the wizard automatically deletes the output sub-device it just created, so no orphan device is left behind. Rebinding the same source+model id+version after unbind reuses the retained (decommissioned, within the recall window) output device via reactivation and preserves its alias unless edited. The write decision uses freshly fetched connector/device state; if an existing binding refers to a missing output device, the wizard stops and asks you to unbind the stale binding instead of creating an orphan replacement. Deleting a binding later follows the normal connector lifecycle; Phase A performs no server-side `model_ref` validation beyond the READY precheck in this wizard.

## Alarms

The platform raises these deduplicated alarms, each automatically cleared when the condition recovers:

- **`GATEWAY_MODEL_FAILED`** (major) — one per model reported `FAILED`.
- **`GATEWAY_MODEL_STALE`** (warning) — one per gateway while its manifest lease is expired or while its `models` segment stays silent for more than 15 minutes; cleared on the next successful model report.
- **`GATEWAY_INFERENCE_RUNTIME`** — major when the runtime reports `OFFLINE`, warning when its health block is silent past three probe intervals.
- **`GATEWAY_CONNECTOR_FAILED` / `GATEWAY_CONNECTOR_DEGRADED`** — the generic connector alarms reused for the binding plane; `DEGRADED` (warning) covers conditions such as `idle-no-inference`.

## Permissions and API

- Viewing the inventory requires the **connector:read** permission: `GET /v1/tenants/{tenant}/gateways/{gateway}/models` returns `[]` (never `null`) for a gateway with no models.
- The view updates live over the single console WebSocket; binding uses the existing device-create and connector-create/update permissions.
