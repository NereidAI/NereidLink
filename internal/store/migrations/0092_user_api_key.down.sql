-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0092_user_api_key.down.sql removes user API-key storage. Append-only audit
-- events are intentionally retained and still identify keys by id and prefix.

DROP TABLE IF EXISTS api_key;
