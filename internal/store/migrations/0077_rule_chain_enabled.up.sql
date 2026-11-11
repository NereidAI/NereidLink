-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0077_rule_chain_enabled.up.sql adds an enable/disable switch to a rule
-- chain group. Previously a chain could only be "stopped" by removing its
-- ACTIVE version, which loses the active version reference. An enabled flag
-- keeps bindings and the active version while suspending execution.
--
-- A disabled group is skipped during per-device chain resolution, so
-- device/profile bindings effectively fall through to the next scope (the
-- tenant default group) instead of executing the bound chain.

ALTER TABLE rule_chain_group
    ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT true;