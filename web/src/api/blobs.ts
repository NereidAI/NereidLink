// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// blobs.ts resolves blob:// telemetry references into short-lived signed
// download URLs. The response also carries size and content type so the
// UI can pick an image, video or text renderer up front.

import client from "./client";
import { currentTenantId } from "./tenant";

// SignedBlob is one resolved media reference.
export interface SignedBlob {
  // URL is the short-lived presigned/signed GET URL.
  url: string;
  // Size is the object byte count.
  size: number;
  // ContentType is the declared or detected media type.
  contentType: string;
}

// BLOB_URI_SCHEME mirrors the constant in sdk/telemetry.
const BLOB_URI_SCHEME = "blob://";

// isBlobValue reports whether a telemetry string is a blob reference.
export function isBlobValue(value: string): boolean {
  return value.startsWith(BLOB_URI_SCHEME);
}

// signBlob asks the platform to sign a GET for one blob object key.
// The key is scoped server side to the given tenant.
export async function signBlob(key: string): Promise<SignedBlob> {
  const { data } = await client.get<{
    url: string;
    size: number;
    content_type: string;
  }>(
    `/v1/tenants/${currentTenantId()}/blob/sign`,
    { params: { key } },
  );
  return { url: data.url, size: data.size, contentType: data.content_type };
}
