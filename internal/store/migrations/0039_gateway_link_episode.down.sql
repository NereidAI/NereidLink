-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0039_gateway_link_episode.down.sql removes the persisted ADR-0026 link
-- watchdog episode and digest bookkeeping. Alarm history created from link
-- advisories remains in the general alarm ledger and is not altered.

DROP TABLE IF EXISTS gateway_link_digest;
DROP TABLE IF EXISTS gateway_link_episode;
