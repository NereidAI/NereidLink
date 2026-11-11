-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0044_connector_manifest.up.sql adds the reported half of ADR-0026 connector
-- manifest reconciliation. Every ~5 minutes a negotiated gateway sends a
-- full-state manifest listing the connector instances it currently has
-- applied (spec_id, applied revision, optional config hash). NL reconciles
-- that reported set against the desired set in connector_instance:
--   * in_sync  - desired and applied at the same revision/content;
--   * missing  - desired but absent from the manifest;
--   * stale    - applied at an older desired revision;
--   * drifted  - same revision but a different effective config hash;
--   * extra    - reported for this gateway but not desired by NL.
-- By default drift only raises an observability alarm; with
-- connector.strict_reconcile enabled NL re-pushes desired and deletes extra
-- instances. These three columns persist the last full-list watermark so a
-- later sweep can detect specs the manifest keeps omitting. The columns are
-- non-destructive additive metadata; existing rows default to "never seen".

ALTER TABLE connector_instance
    ADD COLUMN manifest_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN reported_config_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_manifest_at TIMESTAMPTZ;

-- Reconciliation looks a gateway's desired set up by gateway; the watermark
-- is read to detect specs repeatedly absent from the manifest.
CREATE INDEX IF NOT EXISTS connector_instance_manifest_idx
    ON connector_instance (tenant_id, gateway_id, last_manifest_at);
