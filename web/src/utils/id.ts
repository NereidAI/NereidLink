// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// id.ts generates RFC-4122 v4 identifiers. crypto.randomUUID is available
// only in secure contexts (https or localhost); over a plain-http LAN
// address it is undefined, so fall back to the same id shape built from
// getRandomValues.

// newUUID returns a v4 UUID in every browsing context NL runs in.
export function newUUID(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, "0"));
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex
    .slice(6, 8)
    .join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10, 16).join("")}`;
}