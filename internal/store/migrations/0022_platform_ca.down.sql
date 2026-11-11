-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back the platform trust-anchor table. Device credentials remain;
-- they simply lose the TLS trust pool that could accept them.

DROP TABLE IF EXISTS platform_ca;
