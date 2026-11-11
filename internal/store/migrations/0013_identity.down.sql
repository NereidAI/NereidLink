-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0013: drop the local identity tables and their indexes.
-- All users and sessions are removed; operator-initiated rollback,
-- not routine operation.

DROP TABLE IF EXISTS user_session;
DROP TABLE IF EXISTS app_user;
