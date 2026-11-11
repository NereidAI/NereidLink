-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0051_firmware_package.down.sql reverses platform-hosted firmware OTA.
--
-- Data impact: repository metadata is removed; the firmware blobs in object
-- storage are not deleted by this SQL (the API deletes object + row together
-- during normal operation). Direct-device OTA target rows (gateway_id IS
-- NULL) are removed so the original NOT NULL constraint can be restored.

-- Restore the original item state whitelist (no 'skipped').
ALTER TABLE ota_task_item
    DROP CONSTRAINT IF EXISTS ota_task_item_state_check;
ALTER TABLE ota_task_item
    ADD CONSTRAINT ota_task_item_state_check
    CHECK (state IN ('pending', 'downloading', 'downloaded', 'updating',
                     'succeeded', 'failed'));

-- Remove direct-device targets before re-imposing the gateway requirement.
DELETE FROM ota_task_item WHERE gateway_id IS NULL;
ALTER TABLE ota_task_item
    ALTER COLUMN gateway_id SET NOT NULL;

ALTER TABLE ota_task
    DROP COLUMN IF EXISTS firmware_id;

ALTER TABLE device
    DROP COLUMN IF EXISTS firmware_version;

DROP TABLE IF EXISTS firmware_package;
