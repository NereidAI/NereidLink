// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// system.ts is the platform administration client used by the system
// console: tenant provisioning lifecycle (list/create/retry), tenant
// suspend/resume and tenant member administration. These routes are
// platform-admin scoped even when they carry a tenant id in the path.

import client from "./client";

// ProvisionTask is one tenant provisioning task.
export interface ProvisionTask {
  tenant_id: string;
  tenant_name: string;
  tenant_status: "ACTIVE" | "SUSPENDED";
  status: "PROVISIONING" | "ACTIVE" | "FAILED";
  attempts: number;
  last_error: string;
  run_after: string;
}

// listTenants returns every tenant with its provisioning state.
export async function listTenants(): Promise<ProvisionTask[]> {
  const { data } = await client.get<ProvisionTask[]>("/v1/tenants");
  return data;
}

// createTenant enqueues a new tenant for provisioning.
export async function createTenant(name: string): Promise<ProvisionTask> {
  const { data } = await client.post<ProvisionTask>("/v1/tenants", { name });
  return data;
}

// retryTenant reschedules a failed provisioning task.
export async function retryTenant(tenantId: string): Promise<ProvisionTask> {
  const { data } = await client.post<ProvisionTask>(
    `/v1/tenants/${tenantId}/retry`,
  );
  return data;
}

// suspendTenant immediately suspends an active tenant with a reason.
export async function suspendTenant(
  tenantId: string,
  reason: string,
): Promise<void> {
  await client.post(`/v1/tenants/${tenantId}/suspend`, { reason });
}

// resumeTenant restores a suspended tenant.
export async function resumeTenant(tenantId: string): Promise<void> {
  await client.post(`/v1/tenants/${tenantId}/resume`);
}

// Member is one tenant user management record.
export interface Member {
  user_id: string;
  email: string;
  display_name: string;
  roles: string[];
  /**
   * data_scope narrows which devices the member can reach: TENANT (the whole
   * tenant), CUSTOMER (one customer subtree) or GROUP (one device-group
   * subtree). SELF is not offered because no resource in the current schema
   * is owned by a user, so the server refuses it.
   */
  data_scope: DataScope;
  /** data_scope_id anchors a CUSTOMER or GROUP scope; empty for TENANT. */
  data_scope_id?: string;
  /** data_scope_name is the server-resolved anchor label, for display. */
  data_scope_name?: string;
  disabled: boolean;
  email_verified: boolean;
  is_platform_admin: boolean;
  last_login_at?: string;
  created_at: string;
}

/** DataScope is the membership narrowing level of one member. */
export type DataScope = "TENANT" | "CUSTOMER" | "GROUP";

/**
 * assignableDataScopes are the scopes an operator may assign. SELF is
 * deliberately absent: it is named in ADR-0022 but cannot be honored, so the
 * server rejects it and the UI must not offer it.
 */
export const assignableDataScopes: DataScope[] = [
  "TENANT",
  "CUSTOMER",
  "GROUP",
];

// MemberInput carries the new account fields; an empty password makes
// the server generate one.
export interface MemberInput {
  email: string;
  display_name?: string;
  password?: string;
  roles?: string[];
  /** data_scope defaults to TENANT when omitted. */
  data_scope?: DataScope;
  /** data_scope_id is required for CUSTOMER and GROUP, omitted for TENANT. */
  data_scope_id?: string;
}

// assignableRoles are the tenant-scoped roles an operator may grant;
// they mirror the server-side assignableRoles in the usermgmt service.
// PLATFORM_ADMIN is represented outside memberships and SERVICE_ACCOUNT
// is reserved for future automation identities.
export const assignableRoles = [
  "TENANT_ADMIN",
  "ENGINEER",
  "CUSTOMER_USER",
  "READ_ONLY",
];

// CreateMemberOutcome carries the member and a one-time generated
// password.
export interface CreateMemberOutcome {
  member: Member;
  generated_password?: string;
}

// listMembers returns every member of a tenant.
export async function listMembers(tenantId: string): Promise<Member[]> {
  const { data } = await client.get<Member[]>(
    `/v1/tenants/${tenantId}/members`,
  );
  return data;
}

// createMember opens a tenant user account.
export async function createMember(
  tenantId: string,
  input: MemberInput,
): Promise<CreateMemberOutcome> {
  const { data } = await client.post<CreateMemberOutcome>(
    `/v1/tenants/${tenantId}/members`,
    input,
  );
  return data;
}

// setMemberRoles replaces the role set of one member.
export async function setMemberRoles(
  tenantId: string,
  userId: string,
  roles: string[],
): Promise<Member> {
  const { data } = await client.post<Member>(
    `/v1/tenants/${tenantId}/members/${userId}/roles`,
    { roles },
  );
  return data;
}

// setMemberDataScope replaces the data scope of one member.
//
// The scope and its anchor are always sent together: a narrowing without its
// anchor, or an anchor left behind on a TENANT scope, is rejected by the
// server rather than silently corrected.
export async function setMemberDataScope(
  tenantId: string,
  userId: string,
  dataScope: DataScope,
  dataScopeId?: string,
): Promise<Member> {
  const { data } = await client.post<Member>(
    `/v1/tenants/${tenantId}/members/${userId}/data-scope`,
    { data_scope: dataScope, data_scope_id: dataScopeId ?? "" },
  );
  return data;
}

// resetMemberPassword replaces a member credential; an empty password
// makes the server generate one.
export async function resetMemberPassword(
  tenantId: string,
  userId: string,
  password?: string,
): Promise<string> {
  const { data } = await client.post<{ password: string }>(
    `/v1/tenants/${tenantId}/members/${userId}/password`,
    { password: password ?? "" },
  );
  return data.password;
}

// setMemberEnabled flips the administrative enable switch.
export async function setMemberEnabled(
  tenantId: string,
  userId: string,
  enabled: boolean,
): Promise<Member> {
  const { data } = await client.post<Member>(
    `/v1/tenants/${tenantId}/members/${userId}/enabled`,
    { enabled },
  );
  return data;
}

// removeMember deletes the member and its tenant-local account.
export async function removeMember(
  tenantId: string,
  userId: string,
): Promise<void> {
  await client.delete(`/v1/tenants/${tenantId}/members/${userId}`);
}

// OpsOrder is one high-risk operation order (ADR-0023).
export interface OpsOrder {
  id: string;
  tenant_id?: string;
  action: string;
  summary: string;
  payload: Record<string, unknown>;
  content_hash: string;
  initiator_id?: string;
  initiator_name: string;
  initiator_channel: "WEB" | "CLI";
  status:
    | "PENDING"
    | "APPROVED"
    | "REJECTED"
    | "EXECUTED"
    | "EXPIRED"
    | "CANCELLED";
  reviewer_id?: string;
  reviewer_name: string;
  reviewer_channel: "" | "WEB" | "CLI";
  review_reason: string;
  reviewed_at?: string;
  executed_at?: string;
  exec_result: Record<string, unknown>;
  expires_at: string;
  created_at: string;
}

// listOpsOrders returns orders filtered by status when given.
export async function listOpsOrders(status?: string): Promise<OpsOrder[]> {
  const { data } = await client.get<OpsOrder[]>("/v1/ops/orders", {
    params: status ? { status } : undefined,
  });
  return data;
}

// reviewOpsOrder confirms or refuses an order as the super administrator.
export async function reviewOpsOrder(
  orderId: string,
  decision: "approve" | "reject",
  reason?: string,
): Promise<OpsOrder> {
  const { data } = await client.post<OpsOrder>(
    `/v1/ops/orders/${orderId}/${decision}`,
    { reason: reason ?? "" },
  );
  return data;
}

// AuditEvent is one tamper-evident audit chain row.
export interface AuditEvent {
  seq: number;
  time: string;
  tenant_id?: string;
  actor_id?: string;
  actor_name: string;
  event_type: string;
  action: string;
  resource_type: string;
  resource_id: string;
  details?: Record<string, unknown>;
  ip: string;
  prev_hash: string;
  hash: string;
}

// AuditFilter narrows the platform audit query.
export interface AuditFilter {
  type?: string;
  tenant?: string;
  actor?: string;
  page: number;
  page_size: number;
}

// listAudit returns audit events newest first with their total count.
export async function listAudit(
  filter: AuditFilter,
): Promise<{ events: AuditEvent[]; total: number }> {
  const { data } = await client.get<{ events: AuditEvent[]; total: number }>(
    "/v1/audit",
    { params: filter },
  );
  return data;
}

// AuditVerifyResult is the hash-chain verification outcome.
export interface AuditVerifyResult {
  ok: boolean;
  events_checked: number;
  seals_checked: number;
  broken_at_seq?: number;
  reason?: string;
}

// verifyAudit recomputes the full audit hash chain and daily seals.
export async function verifyAudit(): Promise<AuditVerifyResult> {
  const { data } = await client.post<AuditVerifyResult>("/v1/audit/verify");
  return data;
}
