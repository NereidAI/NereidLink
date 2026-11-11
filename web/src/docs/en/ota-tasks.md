<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Upgrade Tasks

A firmware rollout upgrades many devices to one target version and tracks each
device separately. This page is the platform-side host for firmware over the
air (OTA); devices pull the package themselves, the platform never pushes.

## Package source

Every rollout names exactly one source:

- **Platform package** — an artifact uploaded to the Firmware Packages page.
  The version label is taken from the package.
- **External URL** — a legacy firmware location. You supply the version label,
  an `https` download URL, the SHA-256 digest (64 hex characters) and the size
  in bytes. The device fetches from that URL directly.

A platform package can target gateway sub-devices; their gateway relays the
progress frames, so the per-device view looks the same for both kinds.

## Target selection

Choose targets in one of two ways:

- **Choose devices** — an explicit list. The membership is fixed at creation.
- **Fleet selector** — pick any combination of device model, device group and
  customer; set dimensions are AND-combined. The fleet is resolved once when
  the rollout is created and snapshotted, so devices added to or removed from
  the selection afterwards do not move a running rollout. The selector is
  capped at 1000 devices; the form shows how many active devices currently
  match before you submit.

A platform package bound to one model can only target that model; selecting
an incompatible device (or a selector covering another model) is rejected.
Devices already reporting the target version are never reflashed: they are
created as `skipped / already_current`.

## Canary batching and safety policy

The advanced section opens a rollout gradually:

- **All at once** (default) — every target opens immediately, matching the
  historical single-batch behaviour.
- **Percent per batch** or **devices per batch** — the first batch opens at
  creation; the next batch opens only after the previous one settles plus the
  observation pause. Later batches are held back server-side and are not
  downloadable until they open.
- **Observation pause** — seconds to watch settled results before opening the
  next batch.

Safety tripwires auto-**abort** a rollout and stop further batches:

- failure rate reaching **abort threshold** (default 20% of settled devices),
- **consecutive failures** (default 5),
- the **download stall watchdog** (default 30 minutes without progress),
- the **reboot confirmation watchdog** (default 2 hours).

Devices already applying when an abort trips finish undisturbed; their results
are still collected while the rollout stays `aborted`, and the abort reason is
shown at the top of the drawer. Leave a field empty to inherit the platform
default.

Offline gateways no longer fail a rollout: their devices stay pending and the
platform dispatches them when the gateway reconnects.

## Success criterion

The success criterion decides when a device counts as **succeeded**. It is set
once per rollout and applies to every device in it.

- **Success on device report** (default) — the device reports a successful
  apply, the rollout finalizes immediately.
- **Confirm after reboot** — the device must reconnect and report the running
  version after rebooting. Only then is the rollout marked successful. If the
  device reports a different version the rollout fails with
  `version_mismatch`; if the device never reboots it fails with
  `reboot_timeout`.

Pick *Success on device report* when the devices already report their running
version after every apply. Pick *Confirm after reboot* only when a rollout must
not be called successful until the new image has actually booted.

## List

Each row shows the target version, the aggregate state, progress, the device
counts (total, succeeded, failed, canceled, skipped) and the creation time. A
**Fleet selector** tag marks selector-snapshot rollouts. Open a rollout to see
its per-device progress, the currently open batch (for canary rollouts) and
any abort reason. The state filter narrows the list to one aggregate state,
including `paused` and `aborted`.

## Per-device progress

The drawer lists every targeted device with its gateway (for sub-devices), the
current phase, progress percentage, message and last update. Expanding a device
shows its event timeline: every phase boundary is recorded, so you can see when
it accepted, started downloading, verified, applied and confirmed — plus the
result code and who reported it (platform, device or gateway).

## Device phases

`pending` → `initiated` → `downloading` → `downloaded` → `verified` →
`updating` → `updated` (applied, awaiting reboot) → `rebooting` → `confirmed`.
The terminal states are `succeeded`, `failed`, `canceled` and `skipped`.

High-frequency progress (percentage, bytes) updates the live row but is not
written to the timeline; only phase changes and the final outcome are.

## Pause and resume

Use **Pause** to hold a running rollout: no new batch opens and reconnecting
gateways do not receive new devices, but devices already applying finish
undisturbed. **Resume** re-dispatches the currently open batch. A paused
rollout can also be canceled. Pausing a rollout that is already terminal is
rejected.

## Cancel a rollout

Cancel affects devices by how far they have progressed:

- **pending** devices are **skipped** — they never start.
- Devices still downloading or waiting to apply are **canceled**; a gateway
  stops relaying the download for them.
- Devices already **updating, updated or rebooting** finish undisturbed. Their
  result is still recorded, but the rollout stays canceled.

Canceling is final and idempotent — canceling an already canceled rollout
succeeds without changing anything. Other terminal states reject the request.

## Result codes

A failed device carries a machine-readable result code, such as
`integrity_check_failed`, `out_of_ram`, `flash_full`, `model_mismatch`,
`version_mismatch`, `reboot_timeout` or `download_no_progress`. The code tells
you why a device failed without reading its log.