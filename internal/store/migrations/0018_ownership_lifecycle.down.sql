-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0018: drop the device lifecycle columns first, then the
-- ownership events, claims, groups and customers in reverse dependency
-- order.

DROP INDEX IF EXISTS device_group_ref_idx;
DROP INDEX IF EXISTS device_customer_idx;
DROP INDEX IF EXISTS device_tenant_state_idx;

ALTER TABLE device
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS recall_until,
    DROP COLUMN IF EXISTS archived_at,
    DROP COLUMN IF EXISTS decommissioned_at,
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS state,
    DROP COLUMN IF EXISTS group_id,
    DROP COLUMN IF EXISTS customer_id;

DROP TABLE IF EXISTS device_ownership_event;
DROP TABLE IF EXISTS device_claim;
DROP TABLE IF EXISTS device_group;
DROP TABLE IF EXISTS customer;
