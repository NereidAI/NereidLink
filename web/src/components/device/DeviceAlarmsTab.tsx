// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceAlarmsTab lists recent alarms originating from the device. It is a
// bounded snapshot (latest 100); the alarm center page provides the full
// filtered history.

import { useEffect, useMemo, useState } from "react";
import { Alert, Card, Table } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import StatusBadge from "../StatusBadge";
import FilterSearchBar from "../../components/FilterSearchBar";
import { listDeviceAlarms, type AlarmRow } from "../../api/devices";
import { formatDateTime } from "../../utils/format";

interface DeviceAlarmsTabProps {
  // deviceId is the detailed device.
  deviceId: string;
}

// DeviceAlarmsTab renders the recent alarm table. When the URL carries an
// alarm_id (a "View alarm" deep link), that row is forced visible and
// highlighted regardless of the active keyword/severity/state filters.
export default function DeviceAlarmsTab({ deviceId }: DeviceAlarmsTabProps) {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const targetAlarmID = searchParams.get("alarm_id") ?? "";

  const query = useQuery({
    queryKey: ["device-alarms", deviceId],
    queryFn: () => listDeviceAlarms(deviceId),
  });
  const rows = useMemo(() => query.data ?? [], [query.data]);

  const [keyword, setKeyword] = useState("");
  const [severityFilter, setSeverityFilter] = useState("");
  const [stateFilter, setStateFilter] = useState("");

  // The linked alarm is only useful once the snapshot is loaded. When it is
  // not among the bounded 100 rows, tell the operator instead of showing an
  // empty/filtered table.
  const targetInSnapshot = useMemo(
    () => rows.some((row) => row.alarm_id === targetAlarmID),
    [rows, targetAlarmID],
  );

  // Clear the deep-link targeting (and its query parameter) once the user
  // changes any filter, so it stops forcing one row into view.
  useEffect(() => {
    if (!targetAlarmID) return;
    if (keyword || severityFilter || stateFilter) {
      const next = new URLSearchParams(searchParams);
      next.delete("alarm_id");
      setSearchParams(next, { replace: true });
    }
    // Only react to filter changes; not re-running on searchParams identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keyword, severityFilter, stateFilter]);

  // Severity options are the fixed alarm vocabulary; they are offered even
  // when the snapshot is empty, so an operator can still apply a filter
  // rather than facing an empty dropdown.
  const severityOptions = useMemo(
    () =>
      (["CRITICAL", "MAJOR", "MINOR", "WARNING"] as const).map((severity) => ({
        value: severity,
        label: t(`status.${severity.toLowerCase()}`),
      })),
    [t],
  );

  const stateOptions = useMemo(
    () =>
      (["ACTIVE", "ACK", "CLEARED"] as const).map((state) => ({
        value: state,
        label:
          state === "ACK" ? t("status.ack") : t(`status.${state.toLowerCase()}`),
      })),
    [t],
  );

  // The snapshot is bounded to 100 server-side, so all filtering is
  // client-side. While a deep link targets one alarm, that single row is
  // appended/forced visible even when it would otherwise be filtered out.
  const filteredRows = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    const matches = rows.filter((row) => {
      if (severityFilter && row.severity !== severityFilter) return false;
      if (stateFilter && row.state !== stateFilter) return false;
      if (term && !row.alarm_type.toLowerCase().includes(term)) return false;
      return true;
    });
    if (!targetAlarmID || matches.some((row) => row.alarm_id === targetAlarmID)) {
      return matches;
    }
    const target = rows.find((row) => row.alarm_id === targetAlarmID);
    return target ? [target, ...matches] : matches;
  }, [rows, keyword, severityFilter, stateFilter, targetAlarmID]);

  const columns = [
    { title: t("common.type"), dataIndex: "alarm_type" },
    {
      title: t("common.severity"),
      dataIndex: "severity",
      render: (severity: string) => <StatusBadge status={severity} />,
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      render: (state: string) => <StatusBadge status={state} />,
    },
    {
      title: t("common.started"),
      dataIndex: "started_at",
      render: (time: string) => formatDateTime(time),
    },
    {
      title: t("status.cleared"),
      dataIndex: "cleared_at",
      render: (time?: string) =>
        time ? formatDateTime(time) : "-",
    },
  ];

  return (
    <Card>
      {targetAlarmID && !targetInSnapshot && !query.isFetching ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.deviceDetail.alarms.linkedMissing")}
          description={t("pages.deviceDetail.alarms.linkedMissingHint")}
        />
      ) : null}
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.deviceDetail.alarms.searchPlaceholder")}
        filters={[
          {
            key: "severity",
            placeholder: t("common.severity"),
            value: severityFilter,
            onChange: setSeverityFilter,
            options: severityOptions,
          },
          {
            key: "state",
            placeholder: t("common.state"),
            value: stateFilter,
            onChange: setStateFilter,
            options: stateOptions,
          },
        ]}
      />
      <Table
        rowKey="alarm_id"
        loading={query.isFetching}
        columns={columns}
        dataSource={filteredRows as AlarmRow[]}
        rowClassName={(row) =>
          row.alarm_id === targetAlarmID ? "linked-alarm-row" : ""
        }
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
    </Card>
  );
}
