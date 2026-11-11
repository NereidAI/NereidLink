-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0069_typed_secret_document.down.sql removes the typed-document columns.
-- Typed secrets are discarded; rows created after migration 0069 lose their
-- values while legacy single-value rows return to the 0047 shape untouched.

ALTER TABLE tenant_secret DROP CONSTRAINT IF EXISTS tenant_secret_value_chk;
ALTER TABLE tenant_secret ALTER COLUMN ciphertext SET NOT NULL;
ALTER TABLE tenant_secret
    DROP COLUMN IF EXISTS document_cipher,
    DROP COLUMN IF EXISTS type_version,
    DROP COLUMN IF EXISTS secret_type;
