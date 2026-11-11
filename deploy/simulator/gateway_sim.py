#!/usr/bin/env python3
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

"""Gateway MQTT simulator for NereidLink (ADR-0010 gateway presence).

The simulator authenticates as a *gateway* device (same credential
mechanisms as mqtt_device_sim.py: token / password / jwt / x509, plain TCP
or TLS/mTLS), announces a set of sub-devices on `v1/gateway/connect`, then
periodically publishes batch telemetry on `v1/gateway/telemetry` on their
behalf. It exercises the gateway-proxy telemetry uplink path.

Sub-devices can be given as canonical names or IDs. Names are resolved
through the REST API and every sub-device is verified to belong to the
gateway (its `gateway_id` must equal the gateway's device id); a sub-device
bound to a different gateway is refused and never proxied.

Prerequisites
-------------
- The gateway device must exist with `is_gateway: true`.
- Each sub-device must exist and be bound to the gateway via `gateway_id`.
- The gateway needs a credential of the selected --auth type.

Usage
-----
    # TOKEN auth, sub-devices by canonical name
    ./gateway_sim.py --auth token --token <credential-id> --password <secret> \
        --gateway-id <gateway-device-uuid> \
        --sub-devices sensor-01 sensor-02 \
        --api-email admin@nereidai.local --api-password 'Admin12345!'

    # X509 mTLS, sub-devices by id
    ./gateway_sim.py --auth x509 --token <x509-cred-id> \
        --cert device.pem --key device-key.pem --ca ca.pem \
        --gateway-id <gateway-device-uuid> --sub-devices <sub-id-1> <sub-id-2>

    # One-shot: connect, announce, publish N batches, then disconnect
    ./gateway_sim.py --auth token --token gw-tok --password secret \
        --gateway-id <gw-uuid> --sub-devices sensor-01 --count 3

The batch payload on v1/gateway/telemetry looks like:

    {"<sub-device-id>": {"ts": 1727360000123, "temperature": 21.4, "rssi": -62}}
"""

import argparse
import json
import math
import os
import re
import sys
import time
import urllib.parse
import urllib.request
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mqtt_device_sim import (  # noqa: E402
    MQTTClient,
    DEFAULT_CA,
    build_tls_context,
    parse_duration,
    resolve_password,
)

DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 1883
DEFAULT_TLS_PORT = 8883
DEFAULT_API = "http://127.0.0.1:8080"
DEFAULT_TENANT = "00000000-0000-0000-0000-000000000001"
KEEPALIVE_SEC = 60

_UUID_RE = re.compile(
    r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")


def build_parser():
    """Build the command-line parser with full auth support."""
    parser = argparse.ArgumentParser(
        description="NereidLink gateway MQTT simulator: proxy sub-device "
                    "telemetry and presence (ADR-0010).")
    parser.add_argument("--host", default=DEFAULT_HOST, help="broker host")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT,
                        help="plain TCP port (default %(default)s)")
    parser.add_argument("--tls-port", type=int, default=DEFAULT_TLS_PORT,
                        help="encrypted MQTTS/mTLS port (default %(default)s)")
    parser.add_argument("--tls", action="store_true",
                        help="use the encrypted TLS listener")
    parser.add_argument("--auth", choices=("token", "password", "jwt", "x509"),
                        default="token", help="credential mechanism")
    parser.add_argument("--token", required=True,
                        help="gateway credentials id sent as MQTT username")
    parser.add_argument("--password", default="",
                        help="secret/password for token and password auth")
    parser.add_argument("--client-id", default="nl-gateway-sim",
                        help="MQTT client id; must be unique per running sim")

    # JWT signing options (mirror mqtt_device_sim.py).
    parser.add_argument("--jwt-alg", choices=("HS256", "RS256", "ES256"),
                        default="HS256")
    parser.add_argument("--jwt-secret", default=None)
    parser.add_argument("--jwt-key-file", default=None)
    parser.add_argument("--jwt-ttl", type=int, default=3600)
    parser.add_argument("--iss", default=None)
    parser.add_argument("--sub", default=None)
    parser.add_argument("--aud", default=None)
    parser.add_argument("--nbf", type=int, default=0)

    # X509 mTLS material.
    parser.add_argument("--cert", default=None, help="gateway certificate PEM")
    parser.add_argument("--key", default=None, help="gateway private key PEM")
    parser.add_argument("--ca", default=DEFAULT_CA, help="trusted CA PEM")

    # Gateway and sub-device selection.
    parser.add_argument("--gateway-id", required=True,
                        help="gateway device UUID (must match the credential)")
    parser.add_argument("--sub-devices", nargs="+", required=True,
                        help="sub-device canonical names or UUIDs")

    # REST resolution (needed to turn names into IDs and verify binding).
    parser.add_argument("--api-url", default=DEFAULT_API,
                        help="REST base URL (default %(default)s)")
    parser.add_argument("--tenant-id", default=DEFAULT_TENANT,
                        help="tenant UUID (default %(default)s)")
    parser.add_argument("--api-email", default="admin@nereidai.local",
                        help="REST login email (default %(default)s)")
    parser.add_argument("--api-password", default="Admin12345!",
                        help="REST login password")

    # Rate control.
    rate = parser.add_mutually_exclusive_group()
    rate.add_argument("--interval", type=parse_duration, default=5.0,
                      help="seconds between batches (default 5s)")
    rate.add_argument("--eps", type=float, default=None,
                      help="batches per second; overrides --interval")
    parser.add_argument("--count", type=int, default=0,
                        help="batches to publish, 0 means forever")
    parser.add_argument("--hold", type=parse_duration, default=0,
                        help="keep the gateway connected after publishing")
    parser.add_argument("--keys", nargs="+", default=["temperature", "rssi"],
                        help="measurement keys in each frame (default %(default)s)")
    return parser


class RestClient:
    """Minimal REST client used to resolve sub-devices and verify binding."""

    def __init__(self, base_url, email, password):
        self.base_url = base_url.rstrip("/")
        self.token = self._login(email, password)

    def _login(self, email, password):
        body = json.dumps({"email": email, "password": password}).encode()
        request = urllib.request.Request(
            self.base_url + "/v1/auth/login", data=body, method="POST")
        request.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(request, timeout=10) as response:
            payload = json.loads(response.read().decode("utf-8"))
        return payload["pair"]["access_token"]

    def _get(self, path):
        request = urllib.request.Request(self.base_url + path, method="GET")
        request.add_header("Authorization", "Bearer " + self.token)
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.loads(response.read().decode("utf-8"))

    def device(self, tenant, device_id):
        """Fetch one device by id within a tenant."""
        path = (f"/v1/tenants/{urllib.parse.quote(tenant)}/devices/"
                f"{urllib.parse.quote(device_id)}")
        return self._get(path)

    def device_by_name(self, name, tenant):
        """Resolve a device canonical name within a tenant."""
        query = urllib.parse.urlencode(
            {"search": name, "state": "ACTIVE", "page": 1, "page_size": 100})
        page = self._get(
            f"/v1/tenants/{urllib.parse.quote(tenant)}/devices?{query}")
        for item in page.get("items", []):
            if item["canonical_name"] == name:
                return item
        return None

    def resolve_sub_devices(self, names, tenant, gateway_id):
        """Resolve and verify each sub-device belongs to the gateway.

        Names are matched exactly; UUIDs are fetched directly. A sub-device
        whose gateway_id differs (or that does not exist) is reported and
        skipped so one bad entry never breaks the batch.
        """
        resolved = []
        for entry in names:
            if _UUID_RE.match(entry):
                device = self.device(tenant, entry)
            else:
                device = self.device_by_name(entry, tenant)
            if not device:
                print(f"[warn] sub-device {entry!r} not found; skipped", flush=True)
                continue
            if device.get("gateway_id") != gateway_id:
                print(
                    f"[warn] sub-device {entry!r} ({device['id']}) belongs to "
                    f"gateway {device.get('gateway_id')}, not {gateway_id}; "
                    "skipped", flush=True)
                continue
            resolved.append(device["id"])
        if not resolved:
            raise SystemExit("no usable sub-devices: none resolved or bound "
                             "to the gateway")
        return resolved


def telemetry_frame(keys, step):
    """Build one per-sub-device telemetry frame with a ts field."""
    values = {}
    for index, key in enumerate(keys):
        values[key] = round(
            math.sin(step / 10.0 + index) * 10.0 + 20.0 + index, 2)
    values["ts"] = int(time.time() * 1000)
    return values


def run(args):
    """Connect as a gateway, announce sub-devices and stream their telemetry."""
    rest = RestClient(args.api_url, args.api_email, args.api_password)
    sub_devices = rest.resolve_sub_devices(
        args.sub_devices, args.tenant_id, args.gateway_id)
    print(f"resolved {len(sub_devices)} bound sub-device(s): "
          f"{', '.join(sub_devices)}", flush=True)

    password = resolve_password(args)
    use_tls = args.tls or args.auth == "x509"
    tls_context = build_tls_context(args) if use_tls else None
    port = args.tls_port if use_tls else args.port

    if args.auth == "x509" and not args.ca:
        raise SystemExit("--auth x509 requires --ca <trusted CA PEM>")

    client = MQTTClient(args.host, port, args.client_id, args.token,
                        password, tls_context)
    try:
        client.connect()
    except ConnectionError as error:
        if args.auth != "x509":
            raise
        raise SystemExit(
            f"{error}\n"
            "For --auth x509 the MQTT username must be the credentials_id of "
            "a registered X509 credential and --cert/--key must be that exact "
            "certificate.") from error

    client.publish("v1/gateway/connect", json.dumps({"devices": sub_devices}))
    print(f"[presence] announced sub-devices: {', '.join(sub_devices)}",
          flush=True)

    interval = args.interval if args.eps is None else 1.0 / args.eps
    last_publish_time = 0.0
    last_ping_time = time.time()
    step = 0
    published = 0
    deadline = None

    try:
        while True:
            now = time.time()
            wait_remaining = interval - (now - last_publish_time)
            if wait_remaining <= 0 and (args.count == 0 or published < args.count):
                frame = {
                    device: telemetry_frame(args.keys, step)
                    for device in sub_devices
                }
                client.publish("v1/gateway/telemetry", json.dumps(frame))
                published += 1
                step += 1
                print(f"[telemetry] batch {published}: {len(frame)} sub-device(s)",
                      flush=True)
                last_publish_time = now
                wait_remaining = interval

            client.pump()
            if now - last_ping_time >= KEEPALIVE_SEC:
                client.ping()
                last_ping_time = now

            publishing_done = args.count != 0 and published >= args.count
            if publishing_done:
                if deadline is None:
                    deadline = now + args.hold if args.hold > 0 else now
                if now >= deadline:
                    break
                time.sleep(min(0.1, max(0, deadline - now)))
            else:
                time.sleep(min(0.05, max(0, wait_remaining)))
    finally:
        try:
            client.publish("v1/gateway/disconnect",
                           json.dumps({"devices": sub_devices}))
        except Exception:
            pass
        client.close()
    print("gateway disconnected", flush=True)
    return 0


def main():
    """Parse arguments and run, translating transport errors for operators."""
    parsed = build_parser().parse_args()
    if parsed.eps is not None and parsed.eps <= 0:
        raise SystemExit("--eps must be positive")
    try:
        sys.exit(run(parsed))
    except (ConnectionError, TimeoutError) as error:
        raise SystemExit(
            f"lost the broker connection: {error}\n"
            f"  client id: {parsed.client_id}\n"
            f"  gateway credential: {parsed.token}\n"
            "Common causes:\n"
            "  - wrong --token/--password (CONNACK 4 or 5)\n"
            "  - a second simulator with the same --client-id, which the "
            "presence layer fences; pass a unique --client-id\n"
            "  - the registry gRPC target routed through an HTTP proxy; "
            "start the server without http_proxy or add the registry host "
            "to no_proxy") from None


if __name__ == "__main__":
    main()
