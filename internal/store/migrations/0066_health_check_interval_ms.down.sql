-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0066_health_check_interval_ms.down.sql restores the seconds-based field.
-- This is a best-effort inverse: sub-second intervals cannot be represented
-- in the old contract and collapse via integer division.
UPDATE connector_instance
   SET health_check = (health_check - 'interval_ms')
                          || jsonb_build_object(
                               'interval_s',
                               (health_check ->> 'interval_ms')::int / 1000
                             )
 WHERE health_check ? 'interval_ms';
