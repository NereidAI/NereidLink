-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back only the additive readback projection and delete-outbox metadata.
-- Desired connector rows and their legacy reported lifecycle are preserved.
ALTER TABLE connector_instance DROP COLUMN IF EXISTS observed_hash;
ALTER TABLE connector_instance DROP COLUMN IF EXISTS observed_revision;
ALTER TABLE connector_instance DROP COLUMN IF EXISTS reported_stats;

DROP TABLE IF EXISTS connector_delete_outbox;
