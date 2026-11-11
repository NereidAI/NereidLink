<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Firmware Packages

The firmware repository stores the artifacts that Upgrade Tasks hand to
devices. A package is a binary plus the metadata the platform needs to decide
whether a device may receive it. Uploading a package is the only way to serve
firmware from the platform itself.

## Upload a package

- **Release name** — required, human-readable title of the release (for
  example `Gateway Sensor Firmware`). It identifies the release in lists and
  rollouts independently of the technical version.
- **Version label** — the version string shown on devices and tasks (for
  example `1.4.2`). It is free-form and not parsed for ordering, but it must
  match what a device reports after applying; the platform compares it to
  skip devices that are already current.
- **Release notes** — optional free text describing what is new, fixed or
  known to matter. Hover the release name in the list to read it.
- **Firmware file** — the binary. Uploads are capped at 512 MiB.
- **Device model** — optional. Bind the package to one device model, or leave
  it empty for a model-agnostic package that any model may take. A model-bound
  package is rejected (400) if a rollout would target another model.
- **Mark as recommended stable version** — flags the package as the default
  recommendation. Exactly one stable package per model scope is allowed;
  marking a new one clears the previous star.

## Repository list

Each row shows the release name and version (hover for release notes), the
file name, the device model (or model-agnostic), the size, the SHA-256 digest
and the upload time. The digest is what a device verifies after download; use
it to confirm the file is the one you intended.

## Actions

- **Mark stable / Unmark stable** — set or clear the recommended stable flag.
- **Download** — fetch the stored artifact.
- **Delete** — remove the package. Packages referenced by an active rollout
  cannot be removed while that rollout is still running.

## How packages reach devices

Devices never receive the artifact over the management API. When a rollout
targets a platform package, the platform authorizes the download and the device
streams the file through the authenticated relay, verifying the SHA-256 digest
before applying. If the blob store is not configured, packages cannot be
uploaded and rollouts must use an external URL.
