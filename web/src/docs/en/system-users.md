<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Users & Roles

This page manages the tenant's member accounts: create users, assign roles,
reset credentials, enable or disable accounts, and remove them. A **platform
administrator** may open it for any tenant; a **tenant administrator** manages
the members of their own tenant only. Engineer, customer and read-only
accounts see an authorization notice.

Each member is one login identity plus exactly one membership in this tenant.
Removing a member removes their access to the tenant; the login account is
deleted only when it has no membership left in any tenant (a suite SSO user
with assignments elsewhere keeps their account and other access).

## Roles

Roles are fixed and case-sensitive. A member may hold more than one; the
effective permissions are the union of the roles' permission points.

- **Tenant Admin** — full read/write inside the tenant, including user
  management. Can read all customer data and is auditable.
- **Engineer** — device configuration and commands, profiles, rule chains,
  alarms, dashboards, escalation chains, probes and edge connectors. No user
  management.
- **Customer User** — view and command the devices in scope, acknowledge
  alarms, read notifications and dashboards. No configuration rights.
- **Read-only** — read access across devices, profiles, rule chains, alarms,
  hooks, dashboards and statistics. Nothing can be changed.

Permissions are evaluated as point × resource × data scope. A role grants the
point; the membership's data scope narrows which devices the point applies to.
A Customer User with `device:read` and a CUSTOMER scope sees only their own
customer's devices, not the whole tenant.

## Data scope

Every membership carries one data scope, editable under **Data scope** in the
row actions and settable when a member is created:

- **Whole tenant** — no narrowing; every device of the tenant is reachable.
- **One customer** — only the devices of the chosen customer. Choose the
  customer in the *Scope anchor* picker.
- **One device group** — only the devices of the chosen group **and every group
  beneath it**, so a scope on a parent stays correct as subgroups are added.

The anchor picker switches with the level, because a customer id is not a
valid group id. A *Whole tenant* scope carries no anchor.

`SELF` (resources owned by the user personally) is not offered. No resource in
the current data model is owned by a user, so the scope cannot be honored;
offering it would let you build a request that can only fail.

Widening a scope from a customer or group to the whole tenant grants reach over
every device, so it is treated as a permission change: the previous and new
scope are both written to the audit trail.

A device that belongs to neither a customer nor a group is visible only to a
*Whole tenant* scope.

## Actions

- **New member** — creates the login and its membership. Enter an email, an
  optional display name, an optional password and the roles. Leave the password
  empty to have the platform generate one; it is shown exactly once.
- **Set roles** — replaces the member's role set. At least one role is required.
- **Data scope** — replaces the member's data scope and its anchor.
- **Reset password** — sets a new password (generated if you leave it empty)
  and clears the login lock state. The plaintext is shown once.
- **Enable / Disable** — a disabled account cannot log in; its sessions stop
  working. Disabling is reversible.
- **Remove** — deletes this tenant's membership; the login account is also
deleted only when it has no membership left in any tenant.

## User API keys

Open the **API keys** tab to inspect long-lived `X-Api-Key` credentials owned by
users in this tenant. The table shows the owner, non-secret key prefix, name,
permission scopes, state, expiry and last-used time. Full key material is never
shown here.

- Search by owner, key name or prefix, and filter by active, revoked or expired
  state.
- **Revoke** disables a key immediately while retaining its database row and
  audit history.
- **Delete** removes the row. Append-only audit events still retain the key id
  and public prefix.
- Tenant administrators do not create keys on behalf of another user. Each user
  creates and rotates their own key on **Account Security**. See
  [Integrate with user API keys](help:page/integrate-api-keys) for the request
  contract and permission behavior.

## Protected accounts

Platform administrator accounts cannot be modified here — not their roles,
data scope, enabled state, password or membership. The protection matters
now that member administration is delegated to tenant administrators: a
tenant admin must never be able to seize a platform admin account.

Every action on this page (create, set roles, set data scope, reset password,
enable/disable, remove) is written to the audit trail.

## Live updates

The member list updates live over the WebSocket `member` stream: when another
platform administrator creates a member, changes roles or data scope,
enables or disables an account, or removes one, the table refreshes itself —
there is no refresh button. The member list inside a tenant's drawer in
platform administration updates the same way, including when the tenant being
viewed is not the administrator's own. Password resets do not change the list
and are not pushed. After a reconnect the list is re-fetched to cover frames
missed while offline. A tenant administrator receives only the frames for
their own tenant.