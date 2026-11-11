<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Profile Delivery

**Profile delivery** pushes a tenant device-profile [library](help:page/profiles) entry to a gateway that did not report it, without re-authoring the source text. It implements the shared-library distribution path (ADR-0038): gateway A reports profile `A1`, the platform keeps `A1` as a tenant asset, and an operator pushes the same `A1` to gateway B, which never uploaded it. The platform only references the library entry and its revision; the document is rendered cloud-side in the format each gateway declares in its hello, so the mechanism is not tied to EdgeX.

There are two complementary surfaces.

## Distribute from a profile

Open **Device Profiles**, open a profile, and scroll to the **Distribute to gateways** card.

1. Choose **Distribute to gateway**.
2. Pick an active gateway. Gateways that already hold an explicit target are not listed; a gateway that only holds the profile through a connector can still be made an explicit target. Gateways whose current firmware has not negotiated the feature are suffixed **firmware unsupported** in the picker.
3. Confirm. The desired frame is pushed immediately when the gateway is online; while offline it stays pending and the full desired set is re-sent idempotently after every reconnect.

The card lists every gateway the profile reaches:

- **Distributed** — an explicit distribution edge you created; the row offers **Withdraw**.
- **Connector reference** — the gateway holds the profile because a connector instance references it. This row is read-only: it is not an explicit target, so there is nothing to withdraw here.

Each row shows the state and a revision triple **applied / dispatched / desired**.

## Distribute or watch from a gateway

Open a gateway's detail page and use its **Profile delivery** tab. It lists every library profile the gateway holds with the same state and revision triple, and it is fully manageable:

1. Choose **Distribute profile** and pick a library profile not already distributed to this gateway. A profile the gateway holds only through a connector is not offered as a duplicate but can still be promoted to an explicit target by distributing it.
2. Confirm. The behavior is identical to distributing from the profile page.
3. A row tagged **Distributed** offers **Withdraw**; a row tagged **Connector reference** is read-only because there is no explicit edge to remove.

The tab polls briefly while a row is in flight and otherwise offers **Refresh**; there is no need to keep refreshing after a row reaches **Applied**. Distribution and withdrawal are the same underlying edges regardless of which surface performed them, so the two views always agree.

## States

- **Pending** — the target is recorded but not yet written to the gateway.
- **Dispatching** — the desired frame has been pushed on the transport.
- **Applied** — the gateway reported the profile at the desired revision.
- **Failed** — the gateway rejected the content; hover the tag for its reason.
- **Rejected** — the gateway declined to apply the profile; hover for the reason.
- **Removing / Removed** — withdrawal flow.

A profile stuck in Pending/Dispatching raises a warning alarm; a continuously failed delivery escalates from major to critical, mirroring connector and secret delivery.

## Withdrawal

**Withdraw** removes the explicit distribution edge. The gateway is told to erase its copy only when the explicit edge is gone *and* no connector instance still references the profile; otherwise the profile stays because the connector still needs it. Withdrawing never deletes the tenant library original.

## Firmware capability

A gateway only receives profile frames when its last hello negotiated the `profiles` feature and declared one or more supported `profile_formats` (the built-in formats are the vendor-neutral `nereid-neutral/1` and the EdgeX adapter `edgex-device-profile/3`). When the feature is absent, the gateway tab shows **Gateway firmware does not support profile delivery**: targets can be recorded now and are delivered automatically after a firmware upgrade, never before. Until the edge-side downlink support is deployed, rows normally rest at **Dispatching** because no edge reports **Applied** yet.
