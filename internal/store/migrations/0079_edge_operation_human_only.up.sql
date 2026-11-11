-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0079_edge_operation_human_only.up.sql closes gateway operations (ADR-0035)
-- to machine identities. Dangerous operations require a TOTP step-up a
-- service account cannot perform, so edge_operation:read/exec are human-only
-- and must not be machine-grantable. Strip any explicit grants already held
-- and redefine the scope whitelist CHECK without those two points. The
-- tenant-admin wildcard ('*') still passes the constraint, but the gateway
-- refuses machine principals on the operation routes regardless of scope.

UPDATE service_account
   SET scopes = array_remove(
                    array_remove(scopes, 'edge_operation:read'),
                    'edge_operation:exec')
 WHERE 'edge_operation:read' = ANY(scopes)
    OR 'edge_operation:exec' = ANY(scopes);

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
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
