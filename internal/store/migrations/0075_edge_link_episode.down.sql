-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0075_edge_link_episode.down.sql removes edge-reported link episode
-- support and restores the migration-0039 shape.
--
-- Data handling: rows with source = 'edge' only exist because of this
-- migration (either inserted edge-only episodes or lease rows overwritten
-- with edge ground truth). Their original lease boundaries cannot be
-- reconstructed, so they are deleted before restoring NOT NULL on alarm_id;
-- untouched source = 'lease' rows are preserved.

DROP INDEX IF EXISTS gateway_link_episode_edge_down_idx;

ALTER TABLE gateway_link_episode
    DROP CONSTRAINT IF EXISTS gateway_link_episode_source_check;

DELETE FROM gateway_link_episode WHERE source = 'edge';

ALTER TABLE gateway_link_episode
    DROP COLUMN IF EXISTS edge_down_at,
    DROP COLUMN IF EXISTS source;

ALTER TABLE gateway_link_episode
    ALTER COLUMN alarm_id SET NOT NULL;
