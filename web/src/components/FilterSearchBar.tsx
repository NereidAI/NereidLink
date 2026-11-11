// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// FilterSearchBar renders the standard right-aligned list toolbar: filter
// selects followed by a search input. All list pages share it so layout and
// behavior stay identical.

import { Input, Select, Space } from "antd";
import { SearchOutlined } from "@ant-design/icons";
import type { ReactNode } from "react";

// FilterOption is one option in a filter dropdown.
export interface FilterOption {
  // Value is submitted to the list API.
  value: string;

  // Label is displayed in the dropdown.
  label: string;
}

// FilterField describes one rendered filter dropdown.
export interface FilterField {
  // Key identifies the filter parameter.
  key: string;

  // Placeholder is shown while no value is selected.
  placeholder?: string;

  // Options are the selectable values.
  options: FilterOption[];

  // Value is the currently selected value.
  value: string;

  // onChange receives the new value or "" after clearing.
  onChange: (value: string) => void;
}

interface FilterSearchBarProps {
  // Filters are rendered left to right before the search box.
  filters?: FilterField[];

  // SearchValue binds the name search input.
  searchValue?: string;

  // SearchPlaceholder is shown in an empty search input.
  searchPlaceholder?: string;

  // onSearchChange receives un-debounced input; pages debounce downstream.
  onSearchChange?: (value: string) => void;

  // Extra controls (e.g. a segmented view toggle) rendered before the
  // filters; primary action buttons stay in the page header.
  extra?: ReactNode;
}

// FilterSearchBar renders the toolbar right aligned above the table. It
// wraps on narrow screens but keeps the controls grouped on the right.
export default function FilterSearchBar({
  filters,
  searchValue,
  searchPlaceholder,
  onSearchChange,
  extra,
}: FilterSearchBarProps) {
  return (
    <div
      style={{
        display: "flex",
        justifyContent: "flex-end",
        marginBottom: 12,
      }}
    >
      <Space wrap>
        {extra}
        {filters?.map((filter) => (
          <Select
            key={filter.key}
            allowClear
            showSearch
            optionFilterProp="label"
            filterOption={(input, option) =>
              String(option?.label ?? "")
                .toLowerCase()
                .includes(input.toLowerCase())
            }
            style={{ width: 170 }}
            placeholder={filter.placeholder}
            value={filter.value || undefined}
            onChange={(value) => filter.onChange(value || "")}
            options={filter.options}
          />
        ))}
        {onSearchChange && (
          <Input
            allowClear
            prefix={<SearchOutlined />}
            style={{ width: 240 }}
            placeholder={searchPlaceholder}
            value={searchValue}
            onChange={(event) => onSearchChange(event.target.value)}
          />
        )}
      </Space>
    </div>
  );
}
