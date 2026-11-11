<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Devices

The Devices page lists every registered device in the tenant and its [connection state](help:glossary/presence). Devices are never physically deleted — see [Decommissioning](#decommissioning-instead-of-deleting) below.

## Finding devices

- Type in the search box to filter by canonical name or alias (filtering is server-side).
- Use the selectors to narrow the list in the same order as the table columns: **type** ([gateway](help:glossary/gateway) / [sub-device](help:glossary/subdevice) / standalone Device), **[device profile](help:glossary/profile)**, **customer**, **device group**, **[lifecycle](help:glossary/lifecycle)** and **connection** (Online / Offline / Unknown). Each selector is also type-to-search, so a long list of profiles/customers/groups can be filtered by typing inside the dropdown without scrolling. The **group** selector has no matching column but stays because Customers & Groups pages deep-link into the device list with a group preselected, and it also lets you clear that preselection.
- Rows are ordered newest first (device creation time, descending). The **Created** column shows that creation time; hover the **Connection** badge to see the device's **last activity** time (latest [telemetry](help:glossary/telemetry) or presence boundary, whichever is newer; it falls back to the last connection-change time when a device has no telemetry yet). An alias, when set, is shown under the canonical name.
- The **[Connector](help:glossary/connector)** column shows the edge connector situation: a sub-device row shows its one bound connector instance, with the tag color encoding the reported apply state (applied / creating / dispatched / degraded / failed); hover for the state text and click the tag to jump straight to the owning gateway's **Connectors** tab. A gateway row shows a numeric badge with the number of **gateway-level** (non-sub-device-bound) connector instances on that gateway, also linking to the gateway Connectors tab. Rows with no binding show "-". The type and state refresh live from edge status frames.
- The **lifecycle state** selector opens on **Active** devices. Choose a specific state to narrow further, or clear the selector to list everything — including decommissioned and archived devices, which are tombstone rows the default views hide.
- The **connection** selector filters by the live connection state, independent of lifecycle state. **Unknown** means the device has never produced a presence boundary.

## Creating a device

1. Click **Create device**.
2. The **Canonical name** is generated for you as a technical identifier (`dev-a3f9k2x7`, or `gw-k2x7a3f9` for a gateway): a `dev-`/`gw-` prefix plus a random letter/digit suffix, unique within the tenant. Click the reload button to regenerate it. The field is read-only here; it stays the stable identity used to cross-check names against the edge side.
3. Optionally select a device profile. A device without a profile stores values without descriptor transformation.
4. Optionally set an **alias** (a human-friendly display name shown under the canonical name) and **additional information** (free-form notes, up to 4096 characters). Neither field carries platform semantics; the canonical name remains the device identity.
5. After creation, open the device and add credentials; a device cannot connect until it has at least one valid credential.

## Bulk import

Import many devices at once from a spreadsheet.

1. Download the **XLSX** or **CSV** template — both contain the same columns.
2. Fill in one row per device. Each row must carry a `canonical_name` that is unique within the tenant (the bulk template does not auto-generate names; only the Create-device form does). Only the measurement/profile columns vary per row; a blank `state` uses the platform default.
3. Drop the file onto the import drawer (or click it to browse) and confirm.

Up to 1000 rows and 4 MiB per file. The import is reported per row: successfully created devices and a list of failures with their reasons. A bad row does not abort the whole file.

## Editing

- The edit form changes the profile and the optional **alias** / **additional information**. The canonical name is a generated technical identifier and cannot be edited. Saving with a blank alias or notes clears the stored value.

## Decommissioning instead of deleting

Deleting a device never removes it. The delete action is an alias for **decommission**, which:

- disables the device's credentials instead of deleting them,
- keeps all telemetry, attributes, commands and alarms,
- evicts presence and disconnects the live session, so the device can no longer connect,
- starts a recall window (90 days by default) during which the device can be **reactivate**d and its credentials re-issued.

When the recall window expires, a background sweep moves the device to the **archived** state. Archiving is the end of the lifecycle, not a deletion.

Select several rows and use **Decommission selected** to apply the same operation in bulk.

## Quarantine

Quarantine is a separate, reversible incident-response lock — see the [Quarantine](help:page/quarantine) help topic under Devices.
