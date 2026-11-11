-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0065_connector_health_check.up.sql adds the instance-level liveness probe
-- policy (ADR: connector health_check). Unlike the connector config template,
-- health checking is an operational decision per device: two instances of the
-- same connector type on different sub-devices may need different intervals.
--
-- The field is opaque JSONB on the NL kernel side. The agreed shape delivered
-- to the edge in the connector spec push is:
--
--   {
--     "resource":       "camera_status",  -- south read resource/command name
--     "interval_ms":    30000,            -- probe cadence
--     "timeout_ms":     5000,             -- one-probe read timeout
--     "fail_threshold": 3                 -- consecutive failures -> disconnect
--   }
--
-- A missing key or empty object disables probing for that instance; the edge
-- starts/stops its probe loop whenever the desired value changes. The probe
-- is a READ whose VALUE IS DISCARDED: it must never emit telemetry or a
-- shadow update, it only drives the edge's own connect/disconnect assertion.
ALTER TABLE connector_instance
    ADD COLUMN health_check JSONB NOT NULL DEFAULT '{}'::jsonb;
