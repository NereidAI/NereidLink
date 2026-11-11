-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0062_mark_onvif_family.up.sql backfills the protocol family on edge/import
-- profiles that the minimal discovery frame left blank but whose name clearly
-- identifies an ONVIF camera. The onvif-camera profile reported by NereidEdge
-- ships only deviceResource descriptors with no protocol/connection block, so
-- device_profile.protocol was stored as ''. Tagging it lets the console offer
-- the official device-onvif-camera service dictionary and the "Onvif"
-- connection form without the operator re-selecting the family by hand.
--
-- Data-preservation guarantees:
--   * Only source IN ('edge','import') rows are touched.
--   * Only rows whose protocol is still blank/NULL are filled, so an explicit
--     operator or vendor family is never overwritten.
--   * The name match is exact/token-based ("onvif" as a word) to avoid tagging
--     an unrelated profile whose name merely contains the substring.

UPDATE device_profile
SET protocol = 'onvif',
    updated_at = now()
WHERE source IN ('edge', 'import')
  AND COALESCE(protocol, '') = ''
  -- "onvif" must appear as a whole word (e.g. "onvif-camera", "Camera ONVIF"),
  -- never as a substring of an unrelated token.
  AND lower(name) ~ '\yonvif\y';
