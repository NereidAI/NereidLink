-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0088_business_orchestration.down.sql removes orchestration metadata only.
-- Connector specs, rule-chain versions and forwarders have independent owners
-- and are deliberately preserved by this schema rollback. Existing service
-- accounts lose only the two scopes introduced by this migration.
UPDATE service_account
   SET scopes = array_remove(array_remove(scopes, 'business:read'), 'business:write')
 WHERE 'business:read' = ANY(scopes) OR 'business:write' = ANY(scopes);

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

DROP TABLE IF EXISTS business_expansion;
DROP TABLE IF EXISTS business_instance;
DROP TABLE IF EXISTS business_template;
