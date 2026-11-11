-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0012: drop the provisioning outbox and the tenant status
-- columns. Provisioning history is removed; operator-initiated
-- rollback, not routine operation.

DROP TABLE IF EXISTS tenant_provision_task;

ALTER TABLE tenant
    DROP COLUMN IF EXISTS provisioned_at,
    DROP COLUMN IF EXISTS status_reason,
    DROP COLUMN IF EXISTS status;
