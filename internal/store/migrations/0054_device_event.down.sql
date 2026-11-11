-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0054_device_event.down.sql reverses the general device domain-event
-- channel. Data impact: the append-only device_event history is dropped;
-- there are no other schema dependencies.

DROP TABLE IF EXISTS device_event;
