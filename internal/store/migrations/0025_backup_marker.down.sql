-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Remove backup markers recorded for doctor. Historical backup success
-- history is operational telemetry rather than source data, so it is deleted
-- with the table on rollback.

DROP TABLE IF EXISTS backup_marker;
