-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0029 records gateway devices and their authorized sub-devices
-- (ADR-0010). A gateway remains an ordinary device with its own
-- credential; the is_gateway flag grants gateway topics, while
-- gateway_id binds a passive sub-device to exactly one gateway.
-- Presence for a sub-device is created and removed through the
-- gateway session and does not require a sub-device credential.

ALTER TABLE device
    ADD COLUMN IF NOT EXISTS is_gateway BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE device
    ADD COLUMN IF NOT EXISTS gateway_id UUID REFERENCES device(id);

CREATE INDEX IF NOT EXISTS device_gateway_idx
    ON device (gateway_id)
    WHERE gateway_id IS NOT NULL;
