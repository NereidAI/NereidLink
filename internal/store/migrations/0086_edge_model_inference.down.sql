-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0086_edge_model_inference.down.sql reverses the edge model inference
-- Phase A surface: audit/inventory tables, the negotiated capability column,
-- the system-profile marker, and the seeded detection/v1 profile.

-- Remove seeded detection/v1 descriptors and the system profiles first. A
-- bound virtual subdevice's profile_id only references them ON DELETE SET
-- NULL (0005), so deleting the profile detaches rather than deleting devices;
-- their historical telemetry stays queryable by UUID.
DELETE FROM measurement_descriptor
 WHERE profile_id IN (
     SELECT id FROM device_profile
      WHERE is_system = TRUE AND name = 'detection/v1');
DELETE FROM device_profile WHERE is_system = TRUE AND name = 'detection/v1';

DROP TABLE IF EXISTS gateway_model_inventory_event;
DROP TABLE IF EXISTS gateway_model_inventory;

ALTER TABLE device DROP COLUMN IF EXISTS agent_inference;
ALTER TABLE device_profile DROP COLUMN IF EXISTS is_system;
