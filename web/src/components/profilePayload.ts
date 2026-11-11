// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profilePayload builds a full ProfileInput from a stored Profile so a focused
// editor can PUT only the fields it changes while echoing every descriptor and
// mapping verbatim. The update endpoint replaces the whole profile, so a
// partial payload would otherwise wipe the descriptor set; this helper is the
// single place that guarantees a round-trip preserves identity ids,
// calibration and mappings. It also maps descriptor form rows (with their
// parallel enum_pairs/address arrays) into the descriptor input shape.

import type { Profile, ProfileInput } from "../api/profiles";
import type { DescriptorFormValues } from "./profile/descriptorModel";

// profileToInput echoes a stored profile into the update payload shape. The
// returned object carries descriptor ids at runtime (the API keeps them to
// update in place rather than delete/recreate) even though ProfileInput omits
// them from its type.
export function profileToInput(profile: Profile): ProfileInput {
  return {
    name: profile.name,
    rule_chain_group_id: profile.rule_chain_group_id,
    protocol: profile.protocol,
    connector_type: profile.connector_type,
    connector_type_version: profile.connector_type_version,
    connector_config_template: profile.connector_config_template ?? {},
    connector_mappings: profile.connector_mappings ?? [],
    descriptors: (profile.descriptors ?? []).map((d) => ({ ...d })),
  } as ProfileInput;
}

// descriptorFormValues maps a stored descriptor set into form rows, expanding
// enum_map into the parallel enum_pairs list the Form.List edits.
export function descriptorFormValues(
  profile: Profile | undefined,
): DescriptorFormValues[] {
  return (profile?.descriptors ?? []).map((d) => ({
    id: d.id,
    measurement_key: d.measurement_key,
    transform_type: d.transform_type,
    unit: d.unit,
    scale: d.scale,
    offset: d.offset,
    precision: d.precision,
    expected_period_seconds: d.expected_period_seconds ?? null,
    tolerance_multiplier: d.tolerance_multiplier,
    expression: d.expression,
    breakpoints: d.breakpoints,
    version: d.version,
    data_type: d.data_type,
    read_write: d.read_write || undefined,
    edge_default: d.edge_default || undefined,
    edge_min: d.edge_min ?? undefined,
    edge_max: d.edge_max ?? undefined,
    inactive: d.inactive || false,
    enum_pairs: Object.entries(d.enum_map ?? {}).map(([k, v]) => ({
      k,
      v,
    })),
  }));
}

// descriptorsToInput converts edited form rows plus their parallel address
// objects into the descriptor payload. Ownership (ADR-0032 §5, revised): the
// protocol family and each point's connector address are maintainable on
// EVERY source, but the vendor-owned spec columns
// (data_type/read_write/edge_default/edge_min/edge_max) are submitted only for
// console-authored (manual) profiles; on edge/import profiles they are shown
// read-only and omitted so a save never clobbers the vendor spec.
export function descriptorsToInput(
  rows: DescriptorFormValues[],
  addresses: Record<string, unknown>[],
  specEditable: boolean,
): ProfileInput["descriptors"] {
  return rows.map((d, index) => {
    const enumMap: Record<string, string> = {};
    for (const pair of d.enum_pairs ?? []) {
      if (pair?.k) enumMap[pair.k] = pair.v ?? "";
    }
    const address = addresses[index] ?? {};
    const base = {
      id: d.id,
      measurement_key: d.measurement_key,
      transform_type: d.transform_type,
      unit: d.unit ?? "",
      scale: d.scale ?? 1,
      offset: d.offset ?? 0,
      precision: d.precision ?? -1,
      expected_period_seconds: d.expected_period_seconds ?? null,
      tolerance_multiplier: d.tolerance_multiplier || undefined,
      expression: d.expression ?? "",
      breakpoints: d.breakpoints ?? [],
      enum_map: enumMap,
      address: Object.keys(address).length > 0 ? address : undefined,
    };
    if (!specEditable) return base;
    return {
      ...base,
      data_type: d.data_type || undefined,
      read_write: d.read_write || undefined,
      edge_default: d.edge_default || undefined,
      edge_min: d.edge_min,
      edge_max: d.edge_max,
      inactive: d.inactive || false,
    };
  });
}
