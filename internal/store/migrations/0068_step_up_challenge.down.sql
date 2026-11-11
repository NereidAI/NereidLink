-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0068_step_up_challenge.down.sql removes the step-up challenge table.
-- Outstanding challenges are discarded; protected actions simply become
-- unavailable until their feature tickets are also rolled back.

DROP TABLE IF EXISTS step_up_challenge;
