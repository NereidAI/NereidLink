-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0020 introduces cross-channel dual control for high-risk operations
-- (ADR-0023). A high-risk action is never executed by the person who
-- requests it: the initiator creates a signed operation order, and a
-- different identity reviewing from a different channel (web UI vs ops
-- CLI) approves it only after comparing the stored payload with the
-- content hash. Orders expire unreviewed after their validity window
-- (default 24h); every transition is written to the audit log.

CREATE TABLE IF NOT EXISTS ops_order (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Tenant scope of the action; NULL means a platform-global order.
    tenant_id UUID,
    -- Whitelisted action catalog key (see internal/opscenter).
    action TEXT NOT NULL,
    -- Human-readable summary presented to the reviewer.
    summary TEXT NOT NULL DEFAULT '',
    -- Full request payload; the reviewer UI/CLI shows the complete diff.
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- SHA-256 hex of the canonical action + payload serialization.
    content_hash TEXT NOT NULL,
    -- Initiator identity and the channel it came from (WEB or CLI).
    initiator_id UUID,
    initiator_name TEXT NOT NULL DEFAULT '',
    initiator_channel TEXT NOT NULL
        CHECK (initiator_channel IN ('WEB', 'CLI')),
    -- Lifecycle: PENDING -> APPROVED -> EXECUTED; terminal alternatives
    -- are REJECTED, EXPIRED and CANCELLED. Rows are never deleted.
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING', 'APPROVED', 'REJECTED',
            'EXECUTED', 'EXPIRED', 'CANCELLED'
        )),
    -- Reviewer identity and channel; dual control enforces that both
    -- differ from the initiator's.
    reviewer_id UUID,
    reviewer_name TEXT NOT NULL DEFAULT '',
    reviewer_channel TEXT NOT NULL DEFAULT ''
        CHECK (reviewer_channel IN ('', 'WEB', 'CLI')),
    review_reason TEXT NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ,
    -- Execution outcome recorded once the approved action has run.
    executed_at TIMESTAMPTZ,
    exec_result JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Validity window; the expiry sweep marks stale PENDING rows.
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Operators work the queue filtered by status; reviewers poll PENDING.
CREATE INDEX IF NOT EXISTS ops_order_status_created_idx
    ON ops_order (status, created_at DESC);

CREATE INDEX IF NOT EXISTS ops_order_tenant_idx
    ON ops_order (tenant_id, created_at DESC)
    WHERE tenant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ops_order_initiator_idx
    ON ops_order (initiator_id, created_at DESC);
