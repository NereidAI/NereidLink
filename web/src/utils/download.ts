// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// download.ts offers one helper for saving text material (PEM files)
// through a temporary object URL. It is used for device certificates,
// private keys and CA bundles.

// downloadText saves content as filename with the given MIME type.
export function downloadText(filename: string, content: string, mime = "application/x-pem-file"): void {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
