-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0076_forwarder_buffer_policy.up.sql makes a forwarder's backlog retention
-- operator-configurable (ADR-0018 follow-up). Previously the stream kept up
-- to 100k messages for 7 days, so a long receiver outage produced a large
-- historical burst that was all delivered on reconnect.
--
-- Two per-forwarder bounds now control the JetStream stream:
--   * message_ttl_ms      - how long an undelivered message is retained
--                           before it expires and is dropped.
--   * max_buffer_messages - maximum messages held; when full the oldest are
--                           discarded (discard-old) so producers never block.
-- Defaults are intentionally conservative so recovery sends only recent data.

ALTER TABLE forwarder
    ADD COLUMN message_ttl_ms BIGINT NOT NULL DEFAULT 3600000
        CHECK (message_ttl_ms BETWEEN 1000 AND 2592000000),
    ADD COLUMN max_buffer_messages INTEGER NOT NULL DEFAULT 1000
        CHECK (max_buffer_messages BETWEEN 1 AND 1000000);