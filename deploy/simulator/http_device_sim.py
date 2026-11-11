#!/usr/bin/env python3
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

"""HTTP device simulator for NereidLink, covering every HTTP auth scheme.

The simulator exercises the southbound HTTP ingress:

- a command loop continuously long-polls GET /v1/me/commands, prints every
  received command and POSTs a correlated response to
  /v1/me/cmdres/<command-id>;
- the main loop publishes sine-wave telemetry frames to
  POST /v1/me/telemetry at a fixed interval.

Authentication (--auth):

    bearer  Authorization: Bearer <token> for a passwordless access token
    basic   Authorization: Basic base64(credentials_id:secret) for a
            secret-bearing TOKEN or a PASSWORD credential
    jwt     Authorization: Basic base64(credentials_id:signed JWT)

Only the Python standard library plus jwt_helper (PyJWT for JWT signing)
is used.

Examples:

    # Publish forever as the seeded passwordless device
    ./deploy/simulator/http_device_sim.py

    # TOKEN credential with a server-issued secret
    ./deploy/simulator/http_device_sim.py --auth basic \\
        --token my-token-id --password 'the-secret'

    # PASSWORD credential (presented over HTTP Basic like a TOKEN)
    ./deploy/simulator/http_device_sim.py --auth basic \\
        --token my-password-id --password 'S3cretPass!'

    # JWT credential, HS256
    ./deploy/simulator/http_device_sim.py --auth jwt \\
        --token my-jwt-id --jwt-alg HS256 --jwt-secret 'shared-secret'

Do not run two simulators with the same credentials id at the same time:
the presence layer fences duplicate sessions.
"""

import argparse
import base64
import json
import math
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

from jwt_helper import load_signing_key, sign_device_jwt

# Suffix multipliers accepted by --interval and --hold.
DURATION_UNITS = {"s": 1, "m": 60, "h": 3600}


def parse_duration(value):
    """Parse a duration string into seconds.

    A bare number is read as seconds ('0.3'), so sub-second publish rates
    do not force the caller through --eps. The suffixed forms '30s', '5m'
    and '1h' remain available.
    """
    if value == "0":
        return 0.0
    if not value:
        raise ValueError("duration must not be empty")
    if value[-1] not in DURATION_UNITS:
        try:
            return float(value)
        except ValueError:
            raise ValueError(
                "duration must be a number of seconds or end with s, m or h"
            ) from None
    return float(value[:-1]) * DURATION_UNITS[value[-1]]


class PayloadSource:
    """Custom telemetry payload source with template substitution.

    The payload is a JSON object or an array of objects (cycled frame by
    frame). Template tokens in the raw JSON text are replaced per frame:
    ${i} frame index, ${ts} epoch milliseconds and ${uuid} a random UUID.
    When no custom payload is supplied the sine-wave default frame is used.
    """

    def __init__(self, payload=None, payload_file=None):
        """Parse and validate the payload source, if any."""
        if payload is not None and payload_file is not None:
            raise ValueError("--payload and --payload-file are mutually exclusive")
        self.templates = None
        if payload_file:
            with open(payload_file, "r", encoding="utf-8") as handle:
                raw = handle.read()
        elif payload is not None:
            raw = payload
        else:
            return

        data = json.loads(raw)
        if isinstance(data, list):
            if not data:
                raise ValueError("payload array must not be empty")
            self.templates = [
                json.dumps(item, separators=(",", ":")) for item in data
            ]
        elif isinstance(data, dict):
            self.templates = [json.dumps(data, separators=(",", ":"))]
        else:
            raise ValueError("payload must be a JSON object or array")
        # Render the first frame up front so malformed templates fail at
        # startup instead of after the rate loop begins.
        json.loads(self.render(0))

    def render(self, index):
        """Return the rendered frame text for the given index."""
        template = self.templates[index % len(self.templates)]
        return (
            template.replace("${i}", str(index))
            .replace("${ts}", str(int(time.time() * 1000)))
            .replace("${uuid}", str(uuid.uuid4()))
        )


class HTTPDevice:
    """Minimal HTTP device client for the ingress endpoints."""

    def __init__(self, base_url, auth_header):
        """Store the endpoint and prebuilt authorization header value."""
        self.base_url = base_url.rstrip("/")
        self.auth_header = auth_header

    def _request(self, method, path, body=None):
        """Issue one authenticated request and return decoded JSON, if any."""
        data = None
        headers = {"Authorization": self.auth_header}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            self.base_url + path, data=data, headers=headers, method=method)
        with urllib.request.urlopen(request, timeout=35) as response:
            raw = response.read()
        if not raw:
            return None
        return json.loads(raw)

    def publish_telemetry(self, frame):
        """Publish one telemetry frame; returns the published point count."""
        return self._request("POST", "/v1/me/telemetry", frame)["published"]

    def poll_command(self):
        """Long-poll one command; returns None when the wait window is idle."""
        result = self._request("GET", "/v1/me/commands")
        return result.get("command") if result else None

    def respond(self, command_id, response):
        """Post the correlated command response."""
        self._request("POST", f"/v1/me/cmdres/{command_id}", response)


def command_loop(device, stop_event):
    """Continuously long-poll commands until stop_event is set."""
    while not stop_event.is_set():
        try:
            command = device.poll_command()
        except urllib.error.HTTPError as error:
            print(f"[command] poll failed: HTTP {error.code}")
            time.sleep(1)
            continue
        except (TimeoutError, urllib.error.URLError) as error:
            print(f"[command] poll error: {error}")
            time.sleep(1)
            continue

        if not command:
            continue
        print(f"[command] {command['name']} {command.get('params')}")
        response = {"ok": True, "received": command["name"]}
        try:
            device.respond(command["id"], response)
            print(f"[command] response posted for {command['id']}")
        except urllib.error.HTTPError as error:
            print(f"[command] response failed: HTTP {error.code}")


def telemetry_frame(index):
    """Build one sine-wave temperature/humidity frame for an interval."""
    temperature = 20 + 5 * math.sin(index * math.pi / 10)
    humidity = 60 + 10 * math.cos(index * math.pi / 10)
    return {
        "temperature": round(temperature, 3),
        "humidity": round(humidity, 3),
    }


def build_auth_header(args):
    """Return the Authorization header value for the selected auth type."""
    if args.auth == "bearer":
        return f"Bearer {args.token}"
    password = args.password
    if args.auth == "jwt":
        key = load_signing_key(args.jwt_alg, args.jwt_secret, args.jwt_key_file)
        password = sign_device_jwt(
            key,
            algorithm=args.jwt_alg,
            ttl_seconds=args.jwt_ttl,
            issuer=args.iss,
            subject=args.sub,
            audience=args.aud,
            not_before=args.nbf,
        )
    raw = f"{args.token}:{password}".encode("utf-8")
    return "Basic " + base64.b64encode(raw).decode("ascii")


def main():
    """Parse arguments, run the command loop thread and publish telemetry."""
    parser = argparse.ArgumentParser(description="NereidLink HTTP device simulator")
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--auth", choices=("bearer", "basic", "jwt"),
                        default="bearer", help="HTTP authentication scheme")
    parser.add_argument("--token", default="seed-device-token",
                        help="passwordless token (bearer) or credentials id")
    parser.add_argument("--password", default="",
                        help="secret for basic auth with TOKEN/PASSWORD")

    # JWT signing options.
    parser.add_argument("--jwt-alg", choices=("HS256", "RS256", "ES256"),
                        default="HS256")
    parser.add_argument("--jwt-secret", default=None,
                        help="shared secret for HS256")
    parser.add_argument("--jwt-key-file", default=None,
                        help="PEM private key file for RS256/ES256")
    parser.add_argument("--jwt-ttl", type=int, default=3600)
    parser.add_argument("--iss", default=None, help="issuer claim")
    parser.add_argument("--sub", default=None, help="subject claim")
    parser.add_argument("--aud", default=None, help="audience claim")
    parser.add_argument("--nbf", type=int, default=0,
                        help="not-before offset in seconds")
    rate = parser.add_mutually_exclusive_group()
    rate.add_argument("--interval", default="1s",
                      help="seconds between telemetry frames (default 1s)")
    rate.add_argument("--eps", type=float, default=None,
                      help="telemetry frames per second, e.g. 0.5 or 20; overrides --interval")
    parser.add_argument("--count", type=int, default=0,
                        help="number of telemetry frames; 0 publishes forever")
    parser.add_argument("--hold", default="30s",
                        help="keep polling commands after the last frame")
    parser.add_argument("--payload", default=None,
                        help="custom JSON payload, supports ${i}, ${ts}, ${uuid}")
    parser.add_argument("--payload-file", default=None,
                        help="JSON file: object repeated or array cycled per frame")
    args = parser.parse_args()

    if args.eps is not None:
        if args.eps <= 0:
            parser.error("--eps must be positive")
        interval = 1.0 / args.eps
    else:
        interval = parse_duration(args.interval)
    hold = parse_duration(args.hold)
    payload_source = PayloadSource(args.payload, args.payload_file)
    auth_header = build_auth_header(args)
    device = HTTPDevice(args.base_url, auth_header)

    stop_event = threading.Event()
    commands = threading.Thread(
        target=command_loop, args=(device, stop_event), daemon=True)
    commands.start()

    print(f"connected to {args.base_url} over HTTP as {args.token}")
    print(f"[rate] target interval {interval:.3f}s, EPS {1.0 / interval:.2f}")
    index = 0
    try:
        while args.count == 0 or index < args.count:
            if payload_source.templates:
                frame = json.loads(payload_source.render(index))
            else:
                frame = telemetry_frame(index)
            published = device.publish_telemetry(frame)
            print(f"[telemetry] frame {index + 1}: {frame} -> {published} points")
            index += 1
            if args.count == 0 or index < args.count:
                time.sleep(interval)

        if hold > 0:
            time.sleep(hold)
    except KeyboardInterrupt:
        pass
    finally:
        stop_event.set()


if __name__ == "__main__":
    sys.exit(main())
