// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// protocolDict.ts holds only catalog-independent helper functions (ADR-0033).
// The protocol/device-service dictionary itself is now global managed data
// served by the backend (see api/protocolCatalog.ts and
// useProtocolCatalog.ts): families, transports and structured connection/
// point fields are operator-editable in the "Device Services" page. What
// remains here is pure string handling that needs no catalog data:
//
//   - normalizeProtocolKey: maps EdgeX spelling variants to a canonical key.
//   - sanitizeEdgeName: turns a free-form name into an EdgeX-safe token.

// normalizeProtocolKey maps the assorted protocol spellings seen in EdgeX
// examples to a canonical edge protocol key (e.g. "modbus_tcp" ->
// "modbus-tcp", "opc-ua" -> "opcua"). It is deliberately forgiving: an
// unrecognized value is returned lowercased and untouched.
export function normalizeProtocolKey(raw: string): string {
  const key = raw.trim();
  if (!key) return "";
  const alias: Record<string, string> = {
    modbus_tcp: "modbus-tcp",
    modbus_rtu: "modbus-rtu",
    modbus_ascii: "modbus-ascii",
    "modbus tcp": "modbus-tcp",
    "modbus rtu": "modbus-rtu",
    "modbus ascii": "modbus-ascii",
    MODBUSTCP: "modbus-tcp",
    MODBUSRTU: "modbus-rtu",
    MODBUSASCII: "modbus-ascii",
    "opc-ua": "opcua",
    opc_ua: "opcua",
    OPCUA: "opcua",
  };
  return alias[key] ?? alias[key.toLowerCase()] ?? key;
}

// sanitizeEdgeName turns a free-form profile name into a token usable as an
// EdgeX device/profile name: EdgeX names are restricted to alphanumerics,
// '_' and '-' and must not start/end with a separator. Every other character
// run (spaces included) collapses to a single '-'; an empty result falls back
// to a safe constant. The platform only uses this to seed a blank template;
// the operator can still edit the field freely afterwards.
export function sanitizeEdgeName(raw: string): string {
  const cleaned = raw
    .trim()
    .replace(/[^a-zA-Z0-9_-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "");
  return cleaned || "edge-device";
}
