// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ca.ts is the platform trust-anchor client: list and import CA
// certificates, enable/disable and delete them, and fetch the public CA
// bundle. Routes under /v1/platform are platform-admin scoped; the
// bundle endpoint is unauthenticated.

import client from "./client";

// TrustAnchor is one platform CA certificate.
export interface TrustAnchor {
  id: string;
  label: string;
  subject_cn: string;
  issuer_cn: string;
  not_before: string;
  not_after: string;
  fingerprint_sha256: string;
  pem: string;
  builtin: boolean;
  status: "ACTIVE" | "DISABLED";
  created_at: string;
  updated_at: string;
}

// listTrustAnchors returns every platform CA.
export async function listTrustAnchors(): Promise<TrustAnchor[]> {
  const { data } = await client.get<TrustAnchor[]>("/v1/platform/ca");
  return data;
}

// importTrustAnchor stores a third-party CA certificate.
export async function importTrustAnchor(
  label: string,
  pem: string,
): Promise<TrustAnchor> {
  const { data } = await client.post<TrustAnchor>("/v1/platform/ca", {
    label,
    pem,
  });
  return data;
}

// setTrustAnchorStatus enables or disables a CA.
export async function setTrustAnchorStatus(
  id: string,
  status: "ACTIVE" | "DISABLED",
): Promise<void> {
  await client.post(`/v1/platform/ca/${id}/status`, { status });
}

// deleteTrustAnchor removes a non-builtin CA.
export async function deleteTrustAnchor(id: string): Promise<void> {
  await client.delete(`/v1/platform/ca/${id}`);
}
