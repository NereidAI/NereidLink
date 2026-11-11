// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// format.ts provides compact number, duration, byte-size and localized
// date-time formatting shared across the console.

import i18n from "../i18n";

// format.ts provides compact number formatting for high-volume counters
// such as telemetry throughput, which can reach billions of points per
// day and would overflow a KPI card when rendered as a raw integer.

// compactUnits pairs a power-of-1000 threshold with its suffix.
const compactUnits: { threshold: number; suffix: string }[] = [
  { threshold: 1e12, suffix: "T" },
  { threshold: 1e9, suffix: "B" },
  { threshold: 1e6, suffix: "M" },
  { threshold: 1e3, suffix: "K" },
];

// formatCompact renders a non-negative integer in K/M/B/T notation with
// at most one decimal place. Values below 1000 are returned unchanged.
// For example 1_500_000 becomes "1.5M".
export function formatCompact(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "0";
  }
  for (const unit of compactUnits) {
    if (value >= unit.threshold) {
      const scaled = value / unit.threshold;
      const rounded = Math.round(scaled * 10) / 10;
      return `${rounded}${unit.suffix}`;
    }
  }
  return String(Math.round(value));
}

// formatElapsed renders a millisecond duration in compact units:
// "45s", "12m", "4h 43m", "3d 2h". Durations must be non-negative.
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return "-";
  }
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const rest = minutes % 60;
    return rest ? `${hours}h ${rest}m` : `${hours}h`;
  }
  const days = Math.floor(hours / 24);
  const rest = hours % 24;
  return rest ? `${days}d ${rest}h` : `${days}d`;
}

// formatBytes renders a byte count using binary units (1024) with one decimal
// place, e.g. 1536 becomes "1.5 KiB". It applies to artifact/file sizes only,
// not device measurements whose units come from descriptors.
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) {
    return "-";
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value.toFixed(1)} ${units[unitIndex]}`;
}

// formatDateTime renders a timestamp following the active UI language.
// Bare toLocaleString() uses the browser locale, which can disagree with
// the selected console language; the locale is pinned here so switching
// English/Chinese also switches date-time order. 24-hour time is used in
// both locales for an unambiguous operations console.
export function formatDateTime(value: string | number | Date): string {
  const locale = i18n.language.startsWith("zh") ? "zh-CN" : "en-US";
  return new Date(value).toLocaleString(locale, { hour12: false });
}

// formatDate renders a calendar date only, following the active UI
// language.
export function formatDate(value: string | number | Date): string {
  const locale = i18n.language.startsWith("zh") ? "zh-CN" : "en-US";
  return new Date(value).toLocaleDateString(locale);
}

