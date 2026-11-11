-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0040_connector_instance.down.sql removes the desired/reported connector
-- instance lifecycle state and restores the migration-0037 service-account
-- scope whitelist without the connector permission points. Configuration is
-- non-business data, so dropping the rows loses no telemetry, shadow, alarm
-- or device state.

DROP TABLE IF EXISTS connector_instance;

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
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
