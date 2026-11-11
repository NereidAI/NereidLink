-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0076_forwarder_buffer_policy.down.sql removes the per-forwarder buffer
-- retention columns. The egress stream then falls back to its built-in
-- default limits once the code is also reverted.

ALTER TABLE forwarder
    DROP COLUMN IF EXISTS max_buffer_messages,
    DROP COLUMN IF EXISTS message_ttl_ms;