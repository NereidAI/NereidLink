-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0080_gateway_operation_per_gateway.up.sql moves the authoritative edge
-- operation descriptors from one global, last-write-wins table keyed by
-- (operation, type_version) to one row per advertising gateway keyed by
-- (gateway_id, operation, type_version) (ADR-0035 follow-up). Two gateways
-- advertising the same key no longer overwrite each other, and invocation
-- resolves the descriptor the target gateway itself advertised. The console
-- catalog is an aggregate over these rows; tenant_operation /
-- tenant_operation_disabled stay tenant scoped. device.agent_operations is
-- retained as a fast "what this firmware advertised" stamp.

CREATE TABLE IF NOT EXISTS gateway_operation_advertisement (
    gateway_id    UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
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
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gateway_operation_advertisement_pk
        PRIMARY KEY (gateway_id, operation, type_version)
);

-- Lookup by operation (catalog aggregate) and per-gateway listing.
CREATE INDEX IF NOT EXISTS gateway_operation_advertisement_op_idx
    ON gateway_operation_advertisement (operation, type_version);
CREATE INDEX IF NOT EXISTS gateway_operation_advertisement_gw_idx
    ON gateway_operation_advertisement (gateway_id, operation);

-- Backfill: expand each gateway's advertised-key stamp and join it to the
-- previously-global descriptor. Stamps carry operation keys only (no
-- versions), so every global version row for an advertised key is copied;
-- that is an accepted approximation. Keys without a surviving global
-- descriptor produce no row and are re-advertised on the gateway's next
-- hello (full-set reconciliation).
INSERT INTO gateway_operation_advertisement
    (gateway_id, operation, type_version, interaction, timeout_ms,
     param_schema, result_schema, risk, display_name, description,
     created_at, updated_at)
SELECT d.id,
       g.operation,
       g.type_version,
       g.interaction,
       g.timeout_ms,
       g.param_schema,
       g.result_schema,
       g.risk,
       g.display_name,
       g.description,
       g.created_at,
       g.updated_at
  FROM device d
 CROSS JOIN LATERAL jsonb_array_elements_text(d.agent_operations) AS adv(operation)
  JOIN gateway_operation g ON g.operation = adv.operation
 WHERE d.is_gateway = TRUE;

DROP TABLE IF EXISTS gateway_operation;
