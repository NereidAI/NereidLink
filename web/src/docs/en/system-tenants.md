<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Tenants (Platform)

The **Tenants** page is the platform administration console for creating and managing tenants. It is only available to platform administrators. Tenants represent isolated customer environments; each has its own users, devices, rule chains, and data.

## Overview

A tenant provisioning task tracks progress through states:
- `PROVISIONING` — the platform is creating the tenant's backing resources
- `ACTIVE` — the tenant is fully provisioned and usable
- `SUSPENDED` — the tenant was suspended by a platform administrator
- `FAILED` — provisioning failed; the last error is shown

## Actions

- **Create tenant** — provision a new tenant. You must supply a unique tenant name. The platform records who created it and when.
- **Retry** — retry a failed provisioning task. Useful after fixing an external dependency.
- **Suspend** — suspend an `ACTIVE` tenant. Suspended tenants cannot log in or perform most operations until resumed.
- **Resume** — return a `SUSPENDED` tenant to `ACTIVE`.
- **Members** — manage each tenant's administrator/member accounts (roles, enable/disable, password rotation).

## Members drawer

- Add members by email/name and assign roles.
- Generated passwords are shown **exactly once** and must be copied immediately.
- Disable/enable members and edit their roles without deleting their audit history.
- Password rotation creates a new password (shown once) or rebinds validity as needed.

## Notes

- Suspension is a platform-level administrative control; it does not delete any data. Data retention rules still apply.
- Provisioning is tracked in the tenant provisioning table with attempt counts and last error details for troubleshooting.
- Audit events are written for all tenant lifecycle actions (creation, suspend, resume, member changes).
