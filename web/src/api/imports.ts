// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// imports.ts defines the shared bulk-import contract: the validate/preview
// response returned by every import endpoint and a multipart upload helper.
// Both the device and profile import modals consume these so the
// validate → preview → confirm flow is implemented once.

import client from "./client";

// ImportPreviewRow is one parsed upload row with its validation result.
export interface ImportPreviewRow {
  line: number;
  valid: boolean;
  reason?: string;
  values: Record<string, string>;
}

// ImportPreview is the dry-run result for one upload.
export interface ImportPreview {
  total: number;
  valid: number;
  invalid: number;
  columns: string[];
  rows: ImportPreviewRow[];
}

// multipartUpload posts one file under the "file" field and returns the
// decoded response. Axios sets the multipart boundary from FormData.
export async function multipartUpload<T>(url: string, file: File): Promise<T> {
  const form = new FormData();
  form.append("file", file);
  const { data } = await client.post<T>(url, form);
  return data;
}
