-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0080_gateway_operation_per_gateway.down.sql restores the single global
-- table and folds the per-gateway advertisements back into one row per
-- (operation, type_version), keeping the most recently updated advertiser
-- (the old last-write-wins semantics).

CREATE TABLE IF NOT EXISTS gateway_operation (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation     TEXT NOT NULL,
    type_version  TEXT NOT NULL DEFAULT '',
    interaction   TEXT NOT NULL DEFAULT 'request-response'
                  CHECK (interaction IN ('request-response','one-way')),
    timeout_ms    INTEGER NOT NULL DEFAULT 15000
                  CHECK (timeout_ms BETWEEN 100 AND 60000),
    param_schema  JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk          TEXT NOT NULL DEFAULT 'normal'
                  CHECK (risk IN ('read','normal','dangerous')),
    display_name  JSONB NOT NULL DEFAULT '{}'::jsonb,
    description   JSONB NOT NULL DEFAULT '{}'::jsonb,
    source        TEXT NOT NULL DEFAULT 'edge' CHECK (source = 'edge'),
    registered_by UUID REFERENCES device(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gateway_operation_pk UNIQUE (operation, type_version)
);

INSERT INTO gateway_operation
    (operation, type_version, interaction, timeout_ms, param_schema,
     result_schema, risk, display_name, description, registered_by,
     created_at, updated_at)
SELECT DISTINCT ON (operation, type_version)
       operation, type_version, interaction, timeout_ms, param_schema,
       result_schema, risk, display_name, description, gateway_id,
       created_at, updated_at
  FROM gateway_operation_advertisement
 ORDER BY operation, type_version, updated_at DESC, gateway_id;

DROP TABLE IF EXISTS gateway_operation_advertisement;
