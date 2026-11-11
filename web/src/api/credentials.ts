// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// credentials.ts exposes the ADR-0009 device credential lifecycle API:
// list/create plus enable/disable, rotation, bulk revocation and the
// validity window shown by the device detail credentials tab.

import client from "./client";
import { currentTenantId } from "./tenant";


// tenantBase is the REST prefix for the fixed development tenant.
const tenantBase = () => `/v1/tenants/${currentTenantId()}`;

// CredentialType is the authentication mechanism of one credential.
export type CredentialType = "TOKEN" | "PASSWORD" | "X509" | "JWT";

// CredentialStatus is the enable state; EXPIRED is scanner-owned.
export type CredentialStatus = "ACTIVE" | "DISABLED" | "EXPIRED";

// Credential is one device credential management row. The secret value
// is readable on demand via revealCredential but is absent from lists.
export interface Credential {
  id: string;
  type: CredentialType;
  credentials_id: string;
  // credentials_value is present on a create response for a
  // server-generated secret and on a reveal response; listing omits it.
  credentials_value?: string;
  // issue requests built-in CA issuance for a new X509 credential.
  issue?: boolean;
  // X509 material on create/reveal responses.
  certificate_pem?: string;
  private_key_pem?: string;
  ca_pem?: string;
  // revealable=false marks a one-way hashed PASSWORD row whose plaintext
  // cannot be recovered; absent for list rows.
  revealable?: boolean;
  status: CredentialStatus;
  effective_at?: string;
  expires_at?: string;
  warn_level?: string;
}

// CredentialInput is the create payload; an empty window means
// effective now and never expiring.
export interface CredentialInput {
  type: CredentialType;
  credentials_id: string;
  credentials_value?: string;
  issue?: boolean;
  effective_at?: string;
  expires_at?: string;
  // extra carries JWT claim constraints (iss/sub/aud) and the optional
  // jwks_uri selecting remote asymmetric verification, as string pairs.
  extra?: Record<string, string>;
}

// listCredentials fetches every credential of a device.
export async function listCredentials(deviceId: string): Promise<Credential[]> {
  const { data } = await client.get<Credential[]>(
    `${tenantBase()}/devices/${deviceId}/credentials`,
  );
  return data;
}

// createCredential attaches a new credential to a device.
export async function createCredential(
  deviceId: string,
  input: CredentialInput,
): Promise<Credential> {
  const { data } = await client.post<Credential>(
    `${tenantBase()}/devices/${deviceId}/credentials`,
    input,
  );
  return data;
}

// setCredentialStatus enables or disables one credential; disabling
// ends the device's live session.
export async function setCredentialStatus(
  deviceId: string,
  credentialId: string,
  status: CredentialStatus,
): Promise<Credential> {
  const { data } = await client.post<Credential>(
    `${tenantBase()}/devices/${deviceId}/credentials/${credentialId}/status`,
    { status },
  );
  return data;
}

// RotateInput carries the rotation payload; expires_at rebinds the
// validity end, credentials_value replaces PASSWORD secrets.
export interface RotateInput {
  credentials_value?: string;
  expires_at?: string;
}

// rotateCredential replaces a credential secret/expiry in place and
// forces the device to reconnect.
export async function rotateCredential(
  deviceId: string,
  credentialId: string,
  input: RotateInput,
): Promise<Credential> {
  const { data } = await client.post<Credential>(
    `${tenantBase()}/devices/${deviceId}/credentials/${credentialId}/rotate`,
    input,
  );
  return data;
}

// revokeAllCredentials disables every credential of a device at once.
export async function revokeAllCredentials(
  deviceId: string,
): Promise<{ revoked: number }> {
  const { data } = await client.post<{ revoked: number }>(
    `${tenantBase()}/devices/${deviceId}/credentials/revoke-all`,
  );
  return data;
}

// revealCredential returns the stored value of one credential for
// inspection. A hashed PASSWORD row returns revealable=false.
export async function revealCredential(
  deviceId: string,
  credentialId: string,
): Promise<Credential> {
  const { data } = await client.get<Credential>(
    `${tenantBase()}/devices/${deviceId}/credentials/${credentialId}/secret`,
  );
  return data;
}

// deleteCredential removes one credential immediately; prefer
// disable for rollback-friendly retirement.
export async function deleteCredential(
  deviceId: string,
  credentialId: string,
): Promise<void> {
  await client.delete(
    `${tenantBase()}/devices/${deviceId}/credentials/${credentialId}`,
  );
}
