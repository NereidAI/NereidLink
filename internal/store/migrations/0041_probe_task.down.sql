-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0041_probe_task.down.sql removes on-demand probe task state and restores
-- the service-account scope whitelist to its migration-0040 form (connector
-- points retained, probe points removed). Probe results are short-lived
-- operational data, so dropping the rows loses no device or telemetry state.

DROP TABLE IF EXISTS probe_task_item;
DROP TABLE IF EXISTS probe_task;

ALTER TABLE service_account DROP CONSTRAINT IF EXISTS service_account_scopes_chk;
ALTER TABLE service_account ADD CONSTRAINT service_account_scopes_chk CHECK (
    scopes <@ ARRAY[
        '*',
        'device:read', 'device:write', 'device:command',
        'credential:read', 'credential:write',
        'profile:read', 'profile:write',
        'rulechain:read', 'rulechain:write',
        'alarm:read', 'alarm:ack', 'alarm:assign', 'alarm:comment',
        'hook:read', 'hook:write',
        'dashboard:read', 'dashboard:write',
        'escalation:read', 'escalation:write',
        'connector:read', 'connector:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
