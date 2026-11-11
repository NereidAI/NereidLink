// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceNameCell renders a device link showing its canonical name with
// the alias as a secondary line when present, so identity columns across
// tables stay consistent instead of showing alias or UUID alone.

import { Space, Typography } from "antd";
import { Link } from "react-router-dom";

// DeviceNameCellProps controls rendering.
interface DeviceNameCellProps {
  // id is the device id used for the detail-page link.
  id: string;

  // canonicalName is the platform-assigned canonical name.
  canonicalName: string;

  // alias is the optional human alias shown on the secondary line.
  alias?: string;

  // fallback is shown when canonicalName is empty (e.g. unresolved row).
  fallback?: string;
}

// DeviceNameCell renders the canonical-name/alias combination.
export default function DeviceNameCell({
  id,
  canonicalName,
  alias,
  fallback,
}: DeviceNameCellProps) {
  const primary = canonicalName || fallback || id.slice(0, 8);
  return (
    <Link to={`/devices/${id}`} onClick={(event) => event.stopPropagation()}>
      {alias ? (
        <Space direction="vertical" size={0}>
          <Typography.Text style={{ fontSize: 12 }}>{primary}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {alias}
          </Typography.Text>
        </Space>
      ) : (
        <Typography.Text style={{ fontSize: 12 }}>{primary}</Typography.Text>
      )}
    </Link>
  );
}
