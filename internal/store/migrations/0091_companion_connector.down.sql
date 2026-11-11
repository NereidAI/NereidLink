-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0091_companion_connector.down.sql rolls back the companion connector
-- tables and restores the migration-0088 service-account scope whitelist
-- without the companion permission points.

DROP TABLE IF EXISTS companion_desired_subscription;
DROP TABLE IF EXISTS companion_endpoint;

ALTER TABLE service_account DROP CONSTRAINT service_account_scopes_chk;
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
        'probes:read', 'probes:write',
        'edge_secret:read', 'edge_secret:write',
        'business:read', 'business:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
