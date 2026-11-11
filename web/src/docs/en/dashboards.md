<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Dashboards

A dashboard is a saved grid of widgets that reads live from the platform.
Widgets pull their data over the same WebSocket connection as the rest of the
console — there is no polling.

## Manage dashboards

Each row shows the name, the widget count and the actions.

- **New dashboard** — create an empty dashboard.
- **Edit layout** — open the layout editor: add widgets, move and resize them,
  then save. The saved layout is stored with the dashboard.
- **Rename** — change the display name. The name must be unique in the tenant.
- **Delete** — remove the dashboard and its layout.

## Widget types

- **Timeseries chart** — plots one [measurement](help:glossary/telemetry) key of one device (or the
  aggregate across devices) over a chosen time window.
- **Latest value** — shows the most recent value of a measurement key as a
  single number.
- **Alarm list** — lists recent alarms so an operator sees incidents without
  leaving the dashboard.

## Configure a widget

Every widget takes a title, and the data widgets additionally take a device
(unbound means all devices), a measurement key and, for charts, a time window.
A widget whose measurement has no data renders empty rather than failing.

Dashboards are pushed to every connected session, so an edit is visible without
a reload.