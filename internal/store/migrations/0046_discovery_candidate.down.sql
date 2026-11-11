-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0046_discovery_candidate.down.sql removes the human-approval queue. Any
-- PENDING requests are abandoned (the gateway can re-discover them after the
-- approval feature is disabled); drop the indexes then the table.

DROP INDEX IF EXISTS discovery_candidate_gateway_idx;
DROP INDEX IF EXISTS discovery_candidate_tenant_state_idx;
DROP INDEX IF EXISTS discovery_candidate_one_pending;
DROP TABLE IF EXISTS discovery_candidate;

-- Remove the approval-mode policy extension.
ALTER TABLE discovery_policy DROP CONSTRAINT IF EXISTS discovery_policy_approval_mode_check;
ALTER TABLE discovery_policy DROP COLUMN IF EXISTS approval_mode;
