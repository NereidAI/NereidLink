// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// LatestTelemetryStrip renders the operator-pinned measurements of one
// device as a compact status summary. Pin selection happens in the
// Telemetry → All measurements table, not here; the strip is read-only.
//
// Before the operator pins anything the most recently updated keys
// fill the section. Values flash briefly on change; a value not updated
// within staleAfter renders muted. One header timestamp shows the
// newest update and per-card hover exposes that card's own time.

import { useEffect, useMemo, useRef, useState } from "react";
import { Card, Col, Row, Space, Tag, Tooltip, Typography } from "antd";
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";

import type { TelemetryKey } from "../../api/devices";
import { useDeviceMeasurements } from "../../hooks/useDeviceMeasurements";
import { usePinnedTelemetry } from "../../hooks/usePinnedTelemetry";
import dayjs from "dayjs";
import { formatDateTime } from "../../utils/format";

// DEFAULT_PINNED fills the section before the operator chooses keys.
const DEFAULT_PINNED = 3;

// staleAfter marks values muted when no update arrived in five minutes.
const staleAfter = 5 * 60 * 1000;

// flashDuration bounds the change-highlight animation.
const flashDuration = 700;

interface LatestTelemetryStripProps {
  // deviceId is the detailed device.
  deviceId: string;
}

// formatNumber renders a numeric value with bounded precision.
function formatNumber(value: number): string {
  if (Number.isInteger(value)) return String(value);
  return value.toFixed(2);
}

// valueSignature serializes value carriers so changes are detected.
function valueSignature(key: TelemetryKey): string {
  return [key.v_double, key.v_bool, key.v_string].map(String).join("|");
}

// isStale reports whether the latest update is older than staleAfter.
function isStale(time: string): boolean {
  return Date.now() - new Date(time).getTime() > staleAfter;
}

// LatestTelemetryStrip renders the pinned measurement cards.
export default function LatestTelemetryStrip({ deviceId }: LatestTelemetryStripProps) {
  const { t, i18n } = useTranslation();
  const location = useLocation();
  const { keys } = useDeviceMeasurements(deviceId, "device-latest-strip");
  const { pinned } = usePinnedTelemetry(deviceId);

  // Flash keys whose value changed since the previous render.
  const [flash, setFlash] = useState<ReadonlySet<string>>(new Set());
  const previousRef = useRef<Map<string, string>>(new Map());
  useEffect(() => {
    const changed = new Set<string>();
    for (const key of keys) {
      const signature = valueSignature(key);
      const previous = previousRef.current.get(key.measurement_key);
      if (previous !== undefined && previous !== signature) {
        changed.add(key.measurement_key);
      }
      previousRef.current.set(key.measurement_key, signature);
    }
    if (changed.size === 0) {
      return;
    }
    // Flash state is derived by diffing against an imperative previous-value
    // ref and auto-cleared by the timer below, so the synchronization (and
    // its only state update) belongs to this effect, not render phase.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setFlash(changed);
    const timer = window.setTimeout(() => setFlash(new Set()), flashDuration);
    return () => window.clearTimeout(timer);
  }, [keys]);

  const byKey = useMemo(() => {
    const map = new Map<string, TelemetryKey>();
    for (const key of keys) {
      map.set(key.measurement_key, key);
    }
    return map;
  }, [keys]);

  // pinnedKeys follows stored preference order when customized,
  // otherwise the most recently updated keys fill the default section.
  const pinnedKeys = useMemo(() => {
    if (pinned === undefined) {
      return keys.slice(0, DEFAULT_PINNED);
    }
    return pinned
      .map((name) => byKey.get(name))
      .filter((key): key is TelemetryKey => key !== undefined);
  }, [pinned, keys, byKey]);

  if (keys.length === 0) {
    return null;
  }

  const updatedAt = pinnedKeys[0]?.time ?? keys[0]?.time ?? "";
  const viewAllTarget = {
    pathname: location.pathname,
    search: "?tab=telemetry&view=measurements",
  };

  return (
    <Card
      size="small"
      style={{ marginBottom: 16 }}
      title={
        <Space size={8}>
          <Typography.Text strong>
            {t("pages.deviceDetail.latestTelemetry")}
          </Typography.Text>
          <Tooltip title={t("pages.deviceDetail.live")}>
            <Typography.Text style={{ color: "#52c41a" }}>●</Typography.Text>
          </Tooltip>
        </Space>
      }
      extra={
        <Space size={12}>
          <Typography.Text type="secondary">
            {updatedAt ? formatDateTime(updatedAt) : ""}
          </Typography.Text>
          <Link to={viewAllTarget}>
            {t("pages.deviceDetail.managePins")}
          </Link>
        </Space>
      }
    >
      {pinnedKeys.length === 0 ? (
        <Typography.Text type="secondary">
          {t("pages.deviceDetail.noPinned")}{" "}
          <Link to={viewAllTarget}>
            {t("pages.deviceDetail.managePins")}
          </Link>
        </Typography.Text>
      ) : (
        <Row gutter={[12, 8]}>
          {pinnedKeys.map((key) => (
            <Col key={key.measurement_key} span={4}>
              <PinnedCard
                measurement={key}
                flashing={flash.has(key.measurement_key)}
                language={i18n.language}
              />
            </Col>
          ))}
        </Row>
      )}
    </Card>
  );
}

// PinnedCard renders one large measurement value.
function PinnedCard({
  measurement,
  flashing,
  language,
}: {
  measurement: TelemetryKey;
  flashing: boolean;
  language: string;
}) {
  const stale = isStale(measurement.time);
  const isZh = language.startsWith("zh");
  const cardDate = dayjs(measurement.time).format(
    isZh ? "YYYY/MM/DD" : "M/D/YYYY",
  );
  const cardClock = dayjs(measurement.time).format(
    isZh ? "HH:mm:ss" : "h:mm:ss A",
  );

  return (
    <div
      style={{
        border: "1px solid #f0f0f0",
        borderRadius: 8,
        padding: "8px 12px",
        background: flashing ? "#e6f7ff" : "#fafafa",
        transition: "background-color 0.6s ease",
      }}
    >
      <Typography.Text
        type="secondary"
        style={{ fontSize: 12, color: stale ? "#bfbfbf" : undefined }}
      >
        {measurement.measurement_key}
      </Typography.Text>
      <div style={{ marginTop: 4 }}>
        <MeasurementValue measurement={measurement} stale={stale} large />
      </div>
      <div style={{ marginTop: 6, lineHeight: 1.3 }}>
        <Typography.Text
          type="secondary"
          style={{ display: "block", fontSize: 11, color: stale ? "#bfbfbf" : undefined }}
        >
          {cardDate}
        </Typography.Text>
        <Typography.Text
          type="secondary"
          style={{ display: "block", fontSize: 11, color: stale ? "#bfbfbf" : undefined }}
        >
          {cardClock}
        </Typography.Text>
      </div>
    </div>
  );
}

// MeasurementValue renders the value according to its carrier type.
function MeasurementValue({
  measurement,
  stale,
  large,
}: {
  measurement: TelemetryKey;
  stale: boolean;
  large?: boolean;
}) {
  const { t } = useTranslation();

  if (measurement.value_type === "boolean") {
    return (
      <Tag color={measurement.v_bool ? "green" : "default"}>
        {measurement.v_bool
          ? t("pages.deviceDetail.telemetry.on")
          : t("pages.deviceDetail.telemetry.off")}
      </Tag>
    );
  }
  if (measurement.value_type === "string") {
    const value = measurement.v_string ?? "-";
    return (
      <Tooltip title={value} overlayInnerStyle={{ maxWidth: 480 }}>
        <Typography.Text
          ellipsis
          style={{
            color: stale ? "#bfbfbf" : undefined,
            display: "block",
            maxWidth: "100%",
          }}
          strong={large}
        >
          {value}
        </Typography.Text>
      </Tooltip>
    );
  }
  return (
    <Typography.Text
      strong={large}
      style={{ color: stale ? "#bfbfbf" : undefined }}
    >
      {measurement.v_double === undefined
        ? "-"
        : formatNumber(measurement.v_double)}
      {measurement.unit ? ` ${measurement.unit}` : ""}
    </Typography.Text>
  );
}
