<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Customers and device groups

Customers and device groups organize devices inside one tenant.

## Hierarchy

Tenant → Customers → Devices. Device groups are optional containers under the tenant or a customer.

## Customers

A customer is an organization (for example a client company), with optional contact name, contact email and external ID.

- Assign a device to a customer when creating or editing it, or move it later from the device lifecycle menu (Transfer).
- Click a customer name on this page to list its devices.
- Members with the CUSTOMER_USER role only see devices of the customer they belong to.

## Device groups

A group is a named set of devices. A group may be anchored to a customer and may have a parent group, forming a tree; cycles are rejected.

- Assign devices to a group in the device form.
- Click a group name to list its devices.
- Groups are used for filtering and later batch operations.

## Notes

- One device belongs to at most one customer and one group.
- Customers or groups with assigned devices cannot be deleted; reassign the devices first.
- Unassigned devices stay visible in the full device list.
