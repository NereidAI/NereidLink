-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0069_typed_secret_document.up.sql upgrades the per-tenant vault
-- (migration 0047) from one opaque ciphertext to a typed, sealed document
-- (ADR-0034). A typed secret stores its type/version and one AES-256-GCM
-- ciphertext over the complete JSON document. The legacy single-value path
-- stays fully supported: ciphertext is kept as-is and new columns default
-- to blank/NULL, so existing rows and connector secret:// references need
-- no migration of data.
--
-- There is deliberately no history: create/rotate overwrites in place.

ALTER TABLE tenant_secret
    ADD COLUMN secret_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN type_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN document_cipher TEXT;

-- Document rows need no legacy single-value seal. Every row must still
-- carry at least one of the two ciphertexts.
ALTER TABLE tenant_secret ALTER COLUMN ciphertext DROP NOT NULL;
ALTER TABLE tenant_secret
    ADD CONSTRAINT tenant_secret_value_chk CHECK (
        ciphertext IS NOT NULL OR document_cipher IS NOT NULL
    );
