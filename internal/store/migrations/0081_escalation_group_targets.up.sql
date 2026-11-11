-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0081 adds an optional device-group refinement to escalation targets
-- (ADR-0019). A step may name a device_group: recipients then become the
-- step's role holders whose RBAC data scope covers that group's subtree
-- (scope TENANT, a scope GROUP anchor inside the target subtree or its
-- ancestor line, or a scope CUSTOMER anchoring a customer present in the
-- target subtree) instead of every role holder tenant-wide. The repeated
-- reminders target gets the same optional refinement. A NULL keeps the
-- existing role-wide behavior, so existing chains behave exactly as
-- before. Deleting the group nulls the reference (ON DELETE SET NULL)
-- and silently degrades the target back to role-wide.

ALTER TABLE escalation_step
    ADD COLUMN IF NOT EXISTS target_group_id UUID
        REFERENCES device_group(id) ON DELETE SET NULL;

ALTER TABLE escalation_chain
    ADD COLUMN IF NOT EXISTS repeat_target_group_id UUID
        REFERENCES device_group(id) ON DELETE SET NULL;