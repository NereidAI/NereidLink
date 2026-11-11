-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0044_connector_manifest.down.sql removes the connector manifest reported
-- watermark columns. Manifest metadata is derived from edge reports and is
-- recoverable; dropping it only loses the last-seen watermark, not desired
-- spec or lifecycle state.

DROP INDEX IF EXISTS connector_instance_manifest_idx;

ALTER TABLE connector_instance
    DROP COLUMN IF EXISTS manifest_revision,
    DROP COLUMN IF EXISTS reported_config_hash,
    DROP COLUMN IF EXISTS last_manifest_at;
