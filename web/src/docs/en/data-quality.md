<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Data Quality

Every [telemetry](help:glossary/telemetry) point carries a **quality** marker. It answers a different question than the device's [connection state](help:glossary/presence): connection state tells you whether the device is online, while quality tells you whether an individual value that arrived is trustworthy.

## The three markers

| Marker | Meaning | When it is produced |
| --- | --- | --- |
| `GOOD` | Valid, in range, converted successfully. | The default for every accepted point. |
| `UNCERTAIN` | Questionable but still usable. | Never assigned by the platform — only a tenant's storage hook or script can set it. |
| `BAD` | Invalid or unusable. | Set when a descriptor transform fails, or when the converted value falls outside the descriptor's valid range. |

## How a point is marked

- A well-formed boolean, string or number is `GOOD` by default.
- A number with no matching profile [descriptor](help:glossary/profile) is stored unconverted and stays `GOOD` — the platform does not guess.
- When a profile descriptor applies a transform (linear, lookup table, expression, enum, identity):
  - if the transform raises an error, the point is `BAD`;
  - if the resulting engineering value is outside the descriptor's `valid_min`/`valid_max` (when set), the point is `BAD`;
  - otherwise it is `GOOD`.
- Text results (for example an enum label) always count as in range; only numeric results are range-checked.
- A tenant can rewrite the marker in a **storage hook** script by returning a `quality` field.

The marker is stored with the point, returned by telemetry queries, exposed to [rule chains](help:glossary/rule-chain) as `metadata.quality`, and shown as a colored tag in a device's **All measurements** table (`GOOD` green, otherwise red).

## Why it matters

A device can be perfectly online and still report a value you should not act on — a failed sensor, a value outside its physical range, or a broken calibration expression. Quality lets dashboards, alarms and rule chains tell "a number arrived" apart from "the number is sound", so a faulty reading can be stored for diagnosis without driving an alert.
