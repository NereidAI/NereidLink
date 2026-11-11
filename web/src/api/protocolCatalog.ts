// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// protocolCatalog.ts exposes the global, operator-editable protocol/device
// service catalog (ADR-0033). The rows are global vendor facts shared by all
// tenants; the tenant path only supplies authorization. The catalog drives
// both the visible "Device Services" page and the structured profile
// connector/point forms, replacing the former hardcoded frontend dictionary.

import client from "./client";
import { currentTenantId } from "./tenant";

// FieldKind is the structured control used for one parameter.
export type FieldKind = "string" | "integer" | "number" | "boolean" | "duration" | "select";

// FieldOption is one selectable option for a "select" field.
export interface FieldOption {
  value: string;
  label: string;
}

// FieldSpec describes one connection or point-address parameter. `key` is
// emitted verbatim into the JSON document the edge consumes.
export interface FieldSpec {
  key: string;
  label: string;
  kind: FieldKind;
  required?: boolean;
  placeholder?: string;
  hint?: string;
  options?: FieldOption[];
  // Connection fields only: transports the field applies to ("*" = all).
  transports?: string[];
  min?: number;
  max?: number;
  // Point fields only: value inserted into a freshly created address.
  defaultValue?: unknown;
}

// Transport is one concrete device-level protocol key (a Device.Protocols
// map key) for a family.
export interface Transport {
  value: string;
  label: string;
}

// ProtocolFamily is one catalog row.
export interface ProtocolFamily {
  family: string;
  display_name: string;
  service_name: string;
  protocol_key: string;
  transports: Transport[];
  connection_fields: FieldSpec[];
  point_fields: FieldSpec[];
  default_point_address: Record<string, unknown>;
  doc_url: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

// ProtocolFamilyInput is the create/update payload.
export interface ProtocolFamilyInput {
  family: string;
  display_name: string;
  service_name?: string;
  protocol_key?: string;
  transports?: Transport[];
  connection_fields?: FieldSpec[];
  point_fields?: FieldSpec[];
  default_point_address?: Record<string, unknown>;
  doc_url?: string;
}

// base is the tenant-scoped route prefix; the catalog data itself is global.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/protocol-families`;
}

// listProtocolFamilies fetches all catalog rows, optionally filtered
// case-insensitively by a substring.
export async function listProtocolFamilies(q?: string): Promise<ProtocolFamily[]> {
  const { data } = await client.get<ProtocolFamily[]>(base(), {
    params: q ? { q } : undefined,
  });
  return data;
}

// getProtocolFamily fetches one family by key.
export async function getProtocolFamily(family: string): Promise<ProtocolFamily> {
  const { data } = await client.get<ProtocolFamily>(`${base()}/${family}`);
  return data;
}

// createProtocolFamily adds an operator-defined family.
export async function createProtocolFamily(
  input: ProtocolFamilyInput,
): Promise<ProtocolFamily> {
  const { data } = await client.post<ProtocolFamily>(base(), input);
  return data;
}

// updateProtocolFamily replaces one family's mutable columns.
export async function updateProtocolFamily(
  family: string,
  input: ProtocolFamilyInput,
): Promise<ProtocolFamily> {
  const { data } = await client.put<ProtocolFamily>(`${base()}/${family}`, input);
  return data;
}

// deleteProtocolFamily removes a custom family; builtins are rejected by the
// backend (409 PROTOCOL_FAMILY_BUILTIN).
export async function deleteProtocolFamily(family: string): Promise<void> {
  await client.delete(`${base()}/${family}`);
}
