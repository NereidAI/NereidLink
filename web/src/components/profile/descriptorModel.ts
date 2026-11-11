// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// descriptorModel holds the non-component definitions for descriptor editing:
// the form-row shape and the fixed option lists. They live apart from the
// descriptor card component so fast-refresh keeps a components-only module
// and other modules (payload mapping, detail sections) can import the types.

import type { NeutralType } from "../../api/profiles";

// transformOptions are the descriptor transform types from ADR-0015.
export const transformOptions = [
  { value: "identity", label: "identity" },
  { value: "linear", label: "linear" },
  { value: "table", label: "table" },
  { value: "expression", label: "expression" },
  { value: "enum", label: "enum" },
];

// neutralTypeOptions are the vendor-neutral point datatypes (ADR-0032 §3).
export const neutralTypeOptions: NeutralType[] = [
  "bool",
  "int",
  "uint",
  "float",
  "string",
  "binary",
];

// readWriteOptions are the accepted point access modes.
export const readWriteOptions = ["R", "W", "RW"];

// DescriptorFormValues uses a parallel enum_pairs list because Form.List
// cannot edit a map directly; it is converted to enum_map on submit.
export interface DescriptorFormValues {
  id?: string;
  measurement_key: string;
  transform_type: string;
  unit?: string;
  scale?: number;
  offset?: number;
  precision?: number;
  // ADR-0014 online-but-silent settings; null/undefined period disables.
  expected_period_seconds?: number | null;
  tolerance_multiplier?: number;
  expression?: string;
  breakpoints?: { x: number; y: number }[];
  enum_pairs?: { k: string; v: string }[];
  version?: number;
  // ADR-0032 neutral spec fields (manual profiles only). Address is held in
  // parallel parent state, never the antd form store.
  data_type?: NeutralType;
  read_write?: string;
  edge_default?: string;
  edge_min?: number;
  edge_max?: number;
  inactive?: boolean;
}
