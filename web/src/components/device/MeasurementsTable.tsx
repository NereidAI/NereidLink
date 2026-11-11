// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// MeasurementsTable renders every measurement of one device with its
// latest value, unit, quality and update time. It shows the complete set
// (dozens of points), supports text/quality filtering and sorts newest
// first; the paged table keeps the panel short. It backs the All
// measurements sub-tab.

import { useMemo, useState } from "react";
import {
  Button,
  Card,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  PushpinFilled,
  PushpinOutlined,
} from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import type { TelemetryKey } from "../../api/devices";
import { isBlobValue } from "../../api/blobs";
import BlobValue from "../BlobValue";
import FilterSearchBar from "../FilterSearchBar";
import { useDeviceMeasurements } from "../../hooks/useDeviceMeasurements";
import { usePinnedTelemetry } from "../../hooks/usePinnedTelemetry";

interface MeasurementsTableProps {
  // deviceId is the detailed device.
  deviceId: string;
}

// valueCell renders one measurement value according to its carrier type.
function valueCell(key: TelemetryKey, t: (k: string) => string) {
  if (key.value_type === "boolean") {
    return (
      <Tag color={key.v_bool ? "green" : "default"}>
        {key.v_bool
          ? t("pages.deviceDetail.telemetry.on")
          : t("pages.deviceDetail.telemetry.off")}
      </Tag>
    );
  }
  if (key.value_type === "string") {
    const value = key.v_string ?? "-";
    if (isBlobValue(value)) {
      return <BlobValue value={value} />;
    }
    return (
      <Tooltip title={value} overlayInnerStyle={{ maxWidth: 480 }}>
        <Typography.Text
          ellipsis
          style={{ display: "block", maxWidth: 320 }}
        >
          {value}
        </Typography.Text>
      </Tooltip>
    );
  }
  const number = key.v_double;
  return (
    <Typography.Text strong>
      {number === undefined || number === null ? "-" : number}
      {key.unit ? ` ${key.unit}` : ""}
    </Typography.Text>
  );
}

// formatByLocale renders an absolute timestamp with a format selected
// by the active UI language; the same instant therefore reads natively
// for English and Chinese operators.
function formatByLocale(time: string, language: string): string {
  const format = language.startsWith("zh")
    ? "YYYY/MM/DD HH:mm:ss"
    : "M/D/YYYY, h:mm:ss A";
  return dayjs(time).format(format);
}

// MeasurementsTable renders the full measurement list.
export default function MeasurementsTable({ deviceId }: MeasurementsTableProps) {
  const { t, i18n } = useTranslation();
  const { keys, loading } = useDeviceMeasurements(
    deviceId,
    "device-measurements-table",
  );
  const { isPinned, togglePin } = usePinnedTelemetry(deviceId);
  const [filter, setFilter] = useState("");
  const [qualityFilter, setQualityFilter] = useState("");

  const filtered = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return keys.filter((key) => {
      if (needle && !key.measurement_key.toLowerCase().includes(needle)) {
        return false;
      }
      if (qualityFilter && (key.quality ?? "GOOD") !== qualityFilter) {
        return false;
      }
      return true;
    });
  }, [keys, filter, qualityFilter]);

  const columns: ColumnsType<TelemetryKey> = [
    {
      title: t("pages.deviceDetail.measurements.columns.pin"),
      key: "pin",
      width: 64,
      render: (_, key) => {
        const pinned = isPinned(key.measurement_key);
        return (
          <Tooltip
            title={t(
              pinned
                ? "pages.deviceDetail.unpin"
                : "pages.deviceDetail.pin",
            )}
          >
            <Button
              type="text"
              size="small"
              icon={
                pinned ? (
                  <PushpinFilled style={{ color: "#1677ff" }} />
                ) : (
                  <PushpinOutlined />
                )
              }
              onClick={() => togglePin(key.measurement_key)}
            />
          </Tooltip>
        );
      },
    },
    {
      title: t("pages.deviceDetail.measurements.columns.key"),
      dataIndex: "measurement_key",
      sorter: (a, b) =>
        a.measurement_key.localeCompare(b.measurement_key),
    },
    {
      title: t("pages.deviceDetail.measurements.columns.value"),
      key: "value",
      render: (_, key) => valueCell(key, t),
    },
    {
      title: t("pages.deviceDetail.measurements.columns.quality"),
      key: "quality",
      width: 110,
      render: (_, key) => {
        const quality = key.quality ?? "GOOD";
        return (
          <Tag color={quality === "GOOD" ? "green" : "red"}>{quality}</Tag>
        );
      },
    },
    {
      title: t("pages.deviceDetail.measurements.columns.updated"),
      dataIndex: "time",
      width: 210,
      defaultSortOrder: "descend",
      sorter: (a, b) => a.time.localeCompare(b.time),
      render: (time: string) => (
        <Typography.Text type="secondary">
          {formatByLocale(time, i18n.language)}
        </Typography.Text>
      ),
    },
  ];

  return (
    <Card>
      <Typography.Text strong style={{ display: "block", marginBottom: 12 }}>
        {t("pages.deviceDetail.measurements.heading")}
      </Typography.Text>
      <FilterSearchBar
        searchValue={filter}
        searchPlaceholder={t(
          "pages.deviceDetail.measurements.searchPlaceholder",
        )}
        onSearchChange={setFilter}
        filters={[
          {
            key: "quality",
            value: qualityFilter,
            onChange: setQualityFilter,
            placeholder: t(
              "pages.deviceDetail.measurements.filterQuality",
            ),
            options: [
              { value: "GOOD", label: "GOOD" },
              { value: "BAD", label: "BAD" },
            ],
          },
        ]}
      />
      <Table<TelemetryKey>
        rowKey="measurement_key"
        loading={loading}
        columns={columns}
        dataSource={filtered}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
          hideOnSinglePage: true,
        }}
      />
    </Card>
  );
}
