// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useProtocolCatalog.ts loads the global device-service catalog once and
// offers family lookup helpers. It is the single frontend source of
// structured connection/point fields (ADR-0033): every profile connector or
// point form resolves its fields from here, so an operator's catalog edit is
// reflected everywhere without a rebuild. Unknown families resolve to null
// and callers fall back to free JSON editing.

import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import {
  listProtocolFamilies,
  type FieldSpec,
  type ProtocolFamily,
} from "../../api/protocolCatalog";
import { normalizeProtocolKey } from "./protocolDict";

// CatalogDictionary is one family adapted for the structured forms: the
// stored family plus a camelCase alias for point fields and the default
// point-address builder.
export interface CatalogDictionary extends ProtocolFamily {
  // pointFields aliases the snake_case point_fields for the point form.
  pointFields: FieldSpec[];
  // defaultPointAddress returns the address object a new point starts with.
  defaultPointAddress: () => Record<string, unknown>;
}

// protocolCatalogQueryKey is shared so every consumer hits one cache entry.
export const protocolCatalogQueryKey = ["protocol-families"] as const;

// useProtocolFamilies loads the whole catalog (a small fixed reference set).
export function useProtocolFamilies() {
  return useQuery({
    queryKey: protocolCatalogQueryKey,
    queryFn: () => listProtocolFamilies(),
    staleTime: 5 * 60_000,
  });
}

// adapt wraps a stored family with the pointFields alias and default
// point-address helper.
function adapt(family: ProtocolFamily): CatalogDictionary {
  return {
    ...family,
    pointFields: family.point_fields,
    defaultPointAddress: () => {
      if (
        family.default_point_address &&
        Object.keys(family.default_point_address).length > 0
      ) {
        return { ...family.default_point_address };
      }
      const out: Record<string, unknown> = {};
      for (const field of family.point_fields) {
        if (field.defaultValue !== undefined) out[field.key] = field.defaultValue;
      }
      return out;
    },
  };
}

// useCatalogDictionary resolves a family by its open-vocabulary family key.
export function useCatalogDictionary(family?: string): CatalogDictionary | null {
  const { data } = useProtocolFamilies();
  return useMemo(() => {
    if (!data || !family) return null;
    const found = data.find(
      (f) => f.family === family.trim().toLowerCase(),
    );
    return found ? adapt(found) : null;
  }, [data, family]);
}

// buildKeyToFamilyIndex maps each concrete edge protocol key (and each
// transport value) back to its family, for cases where only a protocols-map
// key is known. Generic keys ("TCP", "other") map via explicit table.
function buildKeyToFamilyIndex(families: ProtocolFamily[]): Map<string, string> {
  const index = new Map<string, string>();
  for (const f of families) {
    if (f.protocol_key) index.set(f.protocol_key, f.family);
    for (const tr of f.transports) index.set(tr.value, f.family);
  }
  return index;
}

// useCatalogDictionaryForKey resolves a family from a concrete device-level
// protocol key (a Device.Protocols map key), tolerating the EdgeX spelling
// variants. Returns null when the key is unknown.
export function useCatalogDictionaryForKey(
  key?: string,
): CatalogDictionary | null {
  const { data } = useProtocolFamilies();
  return useMemo(() => {
    if (!data || !key) return null;
    const byKey = buildKeyToFamilyIndex(data);
    const normalized = normalizeProtocolKey(key);
    const familyKey =
      byKey.get(key) ?? byKey.get(normalized) ?? byKey.get(normalized.toLowerCase());
    if (!familyKey) return null;
    const found = data.find((f) => f.family === familyKey);
    return found ? adapt(found) : null;
  }, [data, key]);
}

// connectionFieldsFor returns the connection fields applicable on one
// concrete transport ("*" fields plus transport-specific ones).
export function connectionFieldsFor(
  dict: CatalogDictionary,
  transport: string,
): FieldSpec[] {
  return dict.connection_fields.filter(
    (field) =>
      (field.transports ?? []).includes("*") ||
      (field.transports ?? []).includes(transport),
  );
}

// familyOptions returns the protocol-picker options (custom values stay
// typeable in the calling AutoComplete).
export function familyOptions(
  families: ProtocolFamily[] | undefined,
): { value: string; label: string }[] {
  return (families ?? [])
    .slice()
    .sort((a, b) => a.display_name.localeCompare(b.display_name))
    .map((f) => ({ value: f.family, label: f.display_name }));
}
