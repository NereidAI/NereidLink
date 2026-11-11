-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Southbound protocol modules are optional add-on processes deployed as
-- separate containers (CoAP, LwM2M, SNMP). Modules are platform-level: one
-- deployment serves every tenant, and the device identity resolved by the
-- adapter determines the tenant. A module registers itself at startup with
-- an enrollment token and renews a heartbeat lease; the core flips stale
-- modules to OFFLINE, and commands for their devices then fail fast with
-- MODULE_OFFLINE.

CREATE TABLE IF NOT EXISTS southbound_module (
    id UUID PRIMARY KEY,
    type TEXT NOT NULL CHECK (type IN ('coap', 'lwm2m', 'snmp')),
    name TEXT NOT NULL UNIQUE,
    ingress_id TEXT NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'OFFLINE'
        CHECK (status IN ('ONLINE', 'OFFLINE')),
    enrollment_token TEXT NOT NULL DEFAULT '',
    registered_at TIMESTAMPTZ,
    last_heartbeat TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Protocol of a device determines which module may own its session.
-- Legacy devices default to MQTT.
ALTER TABLE device ADD COLUMN IF NOT EXISTS protocol TEXT NOT NULL DEFAULT 'MQTT';

-- Protocol-specific credential material that does not fit the flat
-- credentials_id/credentials_value pair, e.g. SNMPv3 USM security level,
-- auth and privacy protocols. {} means no extra material.
ALTER TABLE device_credential ADD COLUMN IF NOT EXISTS extra JSONB
    NOT NULL DEFAULT '{}'::jsonb;
