// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secrets.ts exposes the per-tenant secret vault. A vault entry is either a
// legacy single value (kind=blob) or a typed, sealed JSON document
// (kind=document). Values are write-only: reads return metadata only and
// list endpoints never echo content.

import axios from "axios";
import client from "./client";
import { currentTenantId } from "./tenant";

// SecretKind distinguishes the storage shape.
export type SecretKind = "blob" | "document";

// SecretMetadata is the REST-safe view of one vault entry.
export interface SecretMetadata {
  id: string;
  tenant_id: string;
  name: string;
  kind: SecretKind;
  secret_type: string;
  type_version: string;
  created_at: string;
  updated_at: string;
}

// SecretInput is one create/rotate payload.
export interface SecretInput {
  secret_type?: string;
  type_version?: string;
  document: Record<string, unknown>;
}

// base is the tenant-scoped route prefix.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/secrets`;
}

// listSecrets fetches metadata for the tenant vault.
export async function listSecrets(): Promise<SecretMetadata[]> {
  const { data } = await client.get<{ items: SecretMetadata[] }>(base());
  return data.items;
}

// putSecretDocument creates or rotates one typed document.
export async function putSecretDocument(
  name: string,
  input: SecretInput,
): Promise<SecretMetadata> {
  const { data } = await client.put<SecretMetadata>(
    `${base()}/${encodeURIComponent(name)}`,
    input,
  );
  return data;
}

// deleteSecret removes one vault entry. By default an in-use secret is
// not deleted and the referencing gateways are returned ("blocked"). With
// force + a verified step-up id, delivered copies are withdrawn and the
// vault original is deleted once all bindings are terminal ("deleting" is
// reported while offline gateways still need to confirm removal).

// SecretReference is one gateway still holding a delivered copy.
export interface SecretReference {
  // gateway_id is the gateway device id.
  gateway_id: string
  // gateway_name is the gateway's canonical name.
  gateway_name: string
  // status is the binding's current status.
  status: string
}

// PendingSecretDelete is the 202 response body for a forced deletion.
export interface PendingSecretDelete {
  // secret_name is the vault key being deleted.
  secret_name: string
  // initiated_at is when the forced deletion started.
  initiated_at: string
}

// DeleteSecretResult describes the outcome of one delete attempt.
export type DeleteSecretResult =
  | { outcome: "deleted" }
  | { outcome: "blocked"; references: SecretReference[] }
  | { outcome: "deleting"; pending: PendingSecretDelete }

// deleteSecret attempts to delete one vault secret.
export async function deleteSecret(
  name: string,
  options?: { force?: boolean; step_up_id?: string },
): Promise<DeleteSecretResult> {
  try {
    const status = await client.delete(
      `${base()}/${encodeURIComponent(name)}`,
      {
        data: {
          force: options?.force ?? false,
          step_up_id: options?.step_up_id,
        },
      },
    );
    if (status.status === 202) {
      return {
        outcome: "deleting",
        pending: status.data as PendingSecretDelete,
      };
    }
    return { outcome: "deleted" };
  } catch (error) {
    if (axios.isAxiosError(error) && error.response?.status === 409) {
      const data = error.response.data as {
        references?: SecretReference[]
      };
      return {
        outcome: "blocked",
        references: data.references ?? [],
      };
    }
    throw error;
  }
}
