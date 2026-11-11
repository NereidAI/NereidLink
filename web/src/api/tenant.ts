// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// tenant.ts resolves the tenant scoping ordinary data API calls.
// Tenant users scope to the tenant their account belongs to; platform
// administrators keep the default tenant when browsing operational
// pages. A future tenant switcher can replace the platform fallback
// without touching the API call sites.

import { authStore } from "../store/authStore";

// DEFAULT_TENANT_ID is the fixed development/seed tenant.
export const DEFAULT_TENANT_ID = "00000000-0000-0000-0000-000000000001";

// currentTenantId returns the tenant that should prefix a data call.
// It reads the live session on every invocation, so a login change is
// reflected without reloading modules.
export function currentTenantId(): string {
  const user = authStore.getState().user;
  if (user && !user.is_platform_admin && user.tenant_id) {
    return user.tenant_id;
  }
  return DEFAULT_TENANT_ID;
}

// TENANT_ADMIN_ROLE is the built-in role whose wildcard points include
// member administration (backend PointMembersAdmin).
const TENANT_ADMIN_ROLE = "TENANT_ADMIN";

// canManageMembers reports whether the session may administer members
// of the current tenant: a platform admin (any tenant) or a
// TENANT_ADMIN inside their own tenant.
export function canManageMembers(): boolean {
  const user = authStore.getState().user;
  if (!user) return false;
  if (user.is_platform_admin) return true;
  const roles = user.tenant_roles?.[currentTenantId()] ?? [];
  return roles.includes(TENANT_ADMIN_ROLE);
}
