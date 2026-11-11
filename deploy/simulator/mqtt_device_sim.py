#!/usr/bin/env python3
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

"""MQTT device simulator for NereidLink, covering every credential type.

The simulator connects to the MQTT ingress as a device, publishes
sine-wave (or custom) telemetry at a fixed interval, subscribes to
downlink commands, prints every command and publishes a correlated
response. It implements just enough of MQTT 3.1.1 (QoS 1) to run; JWT
signing uses jwt_helper (PyJWT), everything else is standard library.

Authentication (--auth):

    token     username=credentials_id, password=secret (empty for a
              passwordless access token such as seed-device-token)
    password  username=credentials_id, password=password value
    jwt       username=credentials_id, password=signed JWT (HS256/RS256/ES256)
    x509      connect to the TLS listener with the device certificate
              (mTLS), username=credentials_id

Transport: --tls uses the encrypted MQTTS listener (default port 8883)
with any auth type; --auth x509 implies --tls and also passes the client
certificate. Plain TCP (default 1883) accepts token/password/jwt.

Examples:

    # Passwordless seed device over plain TCP
    ./mqtt_device_sim.py

    # TOKEN credential with a server-issued secret over MQTTS
    ./mqtt_device_sim.py --auth token --tls \\
        --token my-token-id --password 'the-secret'

    # PASSWORD credential
    ./mqtt_device_sim.py --auth password \\
        --token my-password-id --password 'S3cretPass!'

    # JWT credential, HS256
    ./mqtt_device_sim.py --auth jwt --token my-jwt-id \\
        --jwt-alg HS256 --jwt-secret 'shared-secret' --sub device-1

    # JWT credential, RS256
    ./mqtt_device_sim.py --auth jwt --token my-jwt-id \\
        --jwt-alg RS256 --jwt-key-file device-rsa.pem

    # X509 credential (mTLS)
    ./mqtt_device_sim.py --auth x509 --token my-x509-id \\
        --cert device.pem --key device-key.pem --ca ca.pem

Do not run two clients with the same credentials id at the same time:
the presence layer fences duplicate sessions.
"""

import argparse
import json
import math
import os
import re
import socket
import ssl
import struct
import sys
import tempfile
import time
import uuid

from jwt_helper import load_signing_key, sign_device_jwt

# Defaults match configs/dev.yaml and the seeded development data.
DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 1883
DEFAULT_TLS_PORT = 8883
DEFAULT_TOKEN = "seed-device-token"
DEFAULT_CLIENT_ID = "nl-simulator"
# Resolved from this file rather than the working directory so the
# simulator also works when started from inside deploy/simulator.
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_CA = os.path.join(REPO_ROOT, "configs", "pki", "ca.pem")
KEEPALIVE_SEC = 60
# Suffix multipliers accepted by --interval and --hold.
DURATION_UNITS = {"s": 1, "m": 60, "h": 3600}

# PEM shapes the simulator has to reason about when splitting a combined
# certificate+key file.
KEY_BLOCK_TYPES = frozenset(("PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY"))
_PEM_BLOCK_RE = re.compile(r"-----BEGIN ([A-Z0-9 ]+)-----")
_PEM_CERT_RE = re.compile(
    r"-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----\s*", re.DOTALL)


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

# Frozen wire topics from internal/mqttapi/topics.go.
TOPIC_TELEMETRY = "v1/me/telemetry"
FILTER_COMMANDS = "v1/me/cmd/#"
PREFIX_COMMAND = "v1/me/cmd/"
PREFIX_COMMAND_RESPONSE = "v1/me/cmdres/"


def encode_remaining_length(length):
    """Encode an MQTT remaining-length value as its variable-length form."""
    encoded = bytearray()
    while True:
        byte = length % 128
        length //= 128
        if length > 0:
            byte |= 0x80
        encoded.append(byte)
        if length == 0:
            return bytes(encoded)


def packet(packet_type_flags, body):
    """Build a complete MQTT packet from its fixed byte and encoded body."""
    return bytes([packet_type_flags]) + encode_remaining_length(len(body)) + body


def encode_string(value):
    """Encode a UTF-8 string with the two-byte length prefix."""
    raw = value.encode("utf-8")
    return struct.pack("!H", len(raw)) + raw


def parse_duration(value):
    """Parse a duration into seconds.

    Accepts a bare number of seconds ('0.3') as well as the suffixed forms
    '30s', '5m' and '1h'; sub-second rates are common when driving a
    device, and forcing --eps for them is a needless detour.
    """
    if value == "0":
        return 0.0
    if not value:
        raise argparse.ArgumentTypeError("duration must not be empty")
    if value[-1] not in DURATION_UNITS:
        try:
            return float(value)
        except ValueError:
            raise argparse.ArgumentTypeError(
                "duration must be a number of seconds or end with s, m or h"
            ) from None
    return float(value[:-1]) * DURATION_UNITS[value[-1]]


class MQTTClient:
    """Minimal MQTT 3.1.1 client: connect (optionally TLS), QoS 1 pub/sub."""

    def __init__(self, host, port, client_id, username, password="",
                 tls_context=None):
        """Initialize connection parameters; the socket connects in connect()."""
        self.host = host
        self.port = port
        self.client_id = client_id
        self.username = username
        self.password = password
        self.tls_context = tls_context
        self.sock = None
        self.next_packet_id = 0
        self.on_command = None

    def _packet_id(self):
        """Return a fresh non-zero packet identifier."""
        self.next_packet_id = (self.next_packet_id % 65535) + 1
        return self.next_packet_id

    def connect(self):
        """Open the socket (wrapped with TLS when configured) and CONNECT."""
        raw = socket.create_connection((self.host, self.port), timeout=10)
        if self.tls_context is not None:
            # Server hostname verification uses the same host the operator
            # passed on the command line.
            raw = self.tls_context.wrap_socket(raw, server_hostname=self.host)
        self.sock = raw
        self.sock.settimeout(1.0)

        # Connect flags: clean session (bit 1), username (bit 7), and
        # password (bit 6) whenever a password/JWT is carried.
        flags = 0x02 | 0x80
        if self.password:
            flags |= 0x40
        body = (
            encode_string("MQTT")
            + bytes([0x04, flags])
            + struct.pack("!H", KEEPALIVE_SEC)
            + encode_string(self.client_id)
            + encode_string(self.username)
        )
        if self.password:
            body += encode_string(self.password)
        self._send(packet(0x10, body))

        response = self._read_packet()
        if response[0] != 0x20 or response[3] != 0x00:
            # Surface the real CONNACK reason instead of a generic refusal so
            # the operator can tell a bad secret apart from a broken registry
            # connection. Codes: 3 server unavailable, 4 bad username/password,
            # 5 not authorized (including a registry that could not be reached).
            raise ConnectionError(
                f"broker rejected the connection (CONNACK return code {response[3]})"
            )

    def subscribe(self, topic_filter):
        """Subscribe to a topic filter at QoS 1 and wait for SUBACK."""
        body = struct.pack("!H", self._packet_id())
        body += encode_string(topic_filter) + bytes([0x01])
        self._send(packet(0x82, body))

        response = self._read_packet()
        if response[0] != 0x90:
            raise ConnectionError("broker rejected the subscription")

    def publish(self, topic, payload):
        """Publish a payload at QoS 1 and wait for PUBACK."""
        body = encode_string(topic)
        body += struct.pack("!H", self._packet_id())
        body += payload.encode("utf-8") if isinstance(payload, str) else payload
        # Fixed byte: PUBLISH (0x30) with QoS 1 (bit 1 set).
        self._send(packet(0x32, body))

        deadline = time.time() + 10
        while time.time() < deadline:
            response = self._read_packet()
            if response[0] == 0x40:
                return
        raise TimeoutError("PUBACK not received")

    def ping(self):
        """Send a PINGREQ keepalive packet."""
        self._send(bytes([0xE0, 0x00]))

    def close(self):
        """Close the socket without a DISCONNECT packet."""
        if self.sock:
            self.sock.close()

    def pump(self):
        """Process pending broker packets without blocking the rate loop.

        A blocking 1-second recv here used to cap the effective rate at
        ~1 EPS regardless of --eps; the receive is non-blocking and a
        timeout simply means the socket is idle.
        """
        self.sock.settimeout(0)
        try:
            raw = self._read_packet()
        finally:
            self.sock.settimeout(1.0)
        if raw is None:
            return
        packet_type = raw[0] & 0xF0
        if packet_type == 0x30:
            self._handle_publish(raw)

    def _handle_publish(self, raw):
        """Parse an inbound PUBLISH frame and dispatch command frames."""
        offset = 1
        offset += _remaining_length_size(raw, offset)
        topic_length = struct.unpack_from("!H", raw, offset)[0]
        offset += 2
        topic = raw[offset : offset + topic_length].decode("utf-8")
        offset += topic_length

        qos = (raw[0] >> 1) & 0x03
        if qos >= 1:
            packet_id = struct.unpack_from("!H", raw, offset)[0]
            offset += 2
        payload = raw[offset:]

        if qos == 1:
            self._send(packet(0x40, struct.pack("!H", packet_id)))

        if topic.startswith(PREFIX_COMMAND) and self.on_command:
            command_name = topic[len(PREFIX_COMMAND) :]
            self.on_command(command_name, payload)

    def _send(self, data):
        """Send all bytes of a packet."""
        self.sock.sendall(data)

    def _read_exact(self, size):
        """Read exactly size bytes, returning None on a non-blocking timeout."""
        chunks = []
        remaining = size
        while remaining > 0:
            try:
                chunk = self.sock.recv(remaining)
            except (socket.timeout, BlockingIOError):
                if chunks:
                    continue
                return None
            except ssl.SSLWantReadError:
                if chunks:
                    continue
                return None
            if not chunk:
                raise ConnectionError("broker closed the connection")
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)

    def _read_packet(self):
        """Read one complete MQTT packet, or None when the socket is idle."""
        first = self._read_exact(1)
        if first is None:
            return None

        multiplier = 1
        value = 0
        while True:
            byte = self._read_exact(1)[0]
            value += (byte & 0x7F) * multiplier
            if byte & 0x80 == 0:
                break
            multiplier *= 128

        body = self._read_exact(value)
        return first + encode_remaining_length(value) + body


def _remaining_length_size(raw, offset):
    """Return the byte count occupied by the remaining-length field."""
    size = 0
    while True:
        size += 1
        if raw[offset + size - 1] & 0x80 == 0:
            return size


def pem_block_types(path):
    """Return the PEM block labels found in a file, e.g. CERTIFICATE.

    Testers frequently keep the certificate and its private key in one
    file (the nginx layout). OpenSSL refuses such a file as a certificate
    chain, so the labels are needed to split it before use.
    """
    try:
        with open(path, "r", encoding="utf-8") as handle:
            text = handle.read()
    except OSError as error:
        raise SystemExit(f"cannot read {path}: {error}") from error
    return set(_PEM_BLOCK_RE.findall(text))


def certificate_only_pem(path):
    """Return the PEM text of a file with every non-certificate block dropped.

    An unchanged file is returned untouched so the common case keeps using
    the operator's own file; a rewritten one lands in a temporary file that
    lasts for the process lifetime.
    """
    try:
        with open(path, "r", encoding="utf-8") as handle:
            text = handle.read()
    except OSError as error:
        raise SystemExit(f"cannot read {path}: {error}") from error

    blocks = re.findall(_PEM_CERT_RE, text)
    if not blocks:
        raise SystemExit(f"no CERTIFICATE block found in {path}")
    stripped = "".join(blocks)
    if stripped == text:
        return path

    handle = tempfile.NamedTemporaryFile(
        "w", suffix=".pem", prefix="nereidlink-cert-", delete=False)
    handle.write(stripped)
    handle.close()
    return handle.name


def build_tls_context(args):
    """Build the TLS context: verify the server, add a client cert for x509."""
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    if not os.path.isfile(args.ca):
        # Missing trust anchor is an operator error, not a device fault:
        # fail with an actionable message instead of a library traceback.
        raise SystemExit(
            f"CA bundle not found: {args.ca}\n"
            "Create the development PKI first: ./bin/nereidlink pki init "
            "(or pass --ca <path>)."
        )
    context.load_verify_locations(cafile=args.ca)
    if args.auth == "x509":
        if not args.cert:
            raise SystemExit("--auth x509 requires --cert <device certificate PEM>")
        # A combined certificate+key file also satisfies the key argument.
        key_file = args.key
        if not key_file and pem_block_types(args.cert) & KEY_BLOCK_TYPES:
            key_file = args.cert
        if not key_file:
            raise SystemExit(
                "--auth x509 requires --key <private key PEM>, or a combined "
                "certificate+key file"
            )
        cert_file = certificate_only_pem(args.cert)
        try:
            context.load_cert_chain(certfile=cert_file, keyfile=key_file)
        except ssl.SSLError as error:
            raise SystemExit(
                f"TLS rejected the client key material: {error}\n"
                f"  certificate: {args.cert}\n  private key: {key_file}\n"
                "Check that the two files are a matching pair, that the "
                "certificate is PEM encoded, and that the key is PEM "
                "(not DER or an encrypted key without a passphrase)."
            ) from error
        finally:
            # The split copy is only needed while the chain is parsed.
            if cert_file != args.cert:
                os.unlink(cert_file)
    return context


def resolve_password(args):
    """Return the MQTT password material for the selected auth type."""
    if args.auth in ("token", "password"):
        return args.password
    if args.auth == "x509":
        # X509 proves possession with the client certificate; no password.
        return ""
    key = load_signing_key(args.jwt_alg, args.jwt_secret, args.jwt_key_file)
    return sign_device_jwt(
        key,
        algorithm=args.jwt_alg,
        ttl_seconds=args.jwt_ttl,
        issuer=args.iss,
        subject=args.sub,
        audience=args.aud,
        not_before=args.nbf,
    )


def run(args):
    """Run the simulation loop until the frame count and hold time elapse."""
    payload_source = PayloadSource(args.payload, args.payload_file)
    password = resolve_password(args)

    use_tls = args.tls or args.auth == "x509"
    tls_context = build_tls_context(args) if use_tls else None
    port = args.tls_port if use_tls else args.port

    if args.auth == "x509" and args.token == DEFAULT_TOKEN:
        # The seeded token is passwordless, so it authenticates without ever
        # looking at the certificate: the run would appear to succeed while
        # proving nothing about mTLS.
        print(
            f"[warning] --token is still the seeded passwordless token "
            f"({DEFAULT_TOKEN!r}). It authenticates on its own and ignores "
            "the client certificate. Pass --token <X509 credentials id> to "
            "actually exercise certificate authentication.",
            flush=True,
        )

    client = MQTTClient(args.host, port, args.client_id, args.token,
                        password, tls_context)
    try:
        client.connect()
    except ConnectionError as error:
        if args.auth != "x509":
            raise
        raise SystemExit(
            f"{error}\n"
            "For --auth x509 the MQTT username must be the credentials_id of a "
            "registered X509 credential and --cert/--key must be that exact "
            "certificate. Register one in the console (Credentials tab, "
            "built-in CA issuance), or paste the certificate on a new X509 "
            "credential."
        ) from error
    client.subscribe(FILTER_COMMANDS)

    def handle_command(name, payload):
        """Print a received command and publish its correlated response."""
        try:
            frame = json.loads(payload.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            frame = {}
        command_id = frame.get("id", "")
        print(f"[command] {name} id={command_id} params={frame.get('params')}", flush=True)

        if command_id:
            response = json.dumps({"ok": True, "command": name})
            client.publish(PREFIX_COMMAND_RESPONSE + command_id, response)
            print(f"[response] sent for {command_id}", flush=True)

    client.on_command = handle_command

    transport = "mqtts/mtls" if use_tls else "mqtt"
    print(f"connected to {args.host}:{port} over {transport} as {args.client_id}", flush=True)
    print(f"[rate] target interval {args.interval:.3f}s, EPS {1.0 / args.interval:.2f}", flush=True)
    deadline = None
    last_publish_time = 0.0
    last_ping_time = time.time()
    frame = 0

    try:
        while True:
            now = time.time()
            wait_remaining = args.interval - (now - last_publish_time)
            if wait_remaining <= 0 and (args.count == 0 or frame < args.count):
                if payload_source.templates:
                    payload = payload_source.render(frame)
                else:
                    payload = json.dumps(
                        {
                            "temperature": round(20 + 5 * math.sin(frame / 10), 3),
                            "humidity": round(50 + 10 * math.cos(frame / 20), 3),
                        }
                    )
                client.publish(TOPIC_TELEMETRY, payload)
                print(f"[telemetry] frame {frame + 1}: {payload}", flush=True)
                frame += 1
                last_publish_time = now
                wait_remaining = args.interval

            client.pump()

            if now - last_ping_time >= KEEPALIVE_SEC:
                client.ping()
                last_ping_time = now

            publishing_done = args.count != 0 and frame >= args.count
            if publishing_done:
                if deadline is None:
                    deadline = now + args.hold if args.hold > 0 else now
                if now >= deadline:
                    break
                time.sleep(min(0.1, max(0, deadline - now)))
            else:
                # Sleep in small steps so inbound commands stay responsive.
                time.sleep(min(0.05, max(0, wait_remaining)))
    finally:
        client.close()
    return 0


def parse_args(argv):
    """Parse command-line arguments with the development defaults."""
    parser = argparse.ArgumentParser(description="NereidLink MQTT device simulator")
    parser.add_argument("--host", default=DEFAULT_HOST)
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help="plain TCP port")
    parser.add_argument("--tls-port", type=int, default=DEFAULT_TLS_PORT,
                        help="encrypted MQTTS/mTLS port")
    parser.add_argument("--tls", action="store_true",
                        help="use the encrypted TLS listener")
    parser.add_argument("--auth", choices=("token", "password", "jwt", "x509"),
                        default="token", help="credential mechanism")
    parser.add_argument("--token", default=DEFAULT_TOKEN,
                        help="credentials id sent as MQTT username")
    parser.add_argument("--password", default="",
                        help="secret/password for token and password auth")
    parser.add_argument("--client-id", default=DEFAULT_CLIENT_ID)

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

    # X509 mTLS material.
    parser.add_argument("--cert", default=None, help="device certificate PEM")
    parser.add_argument("--key", default=None, help="device private key PEM")
    parser.add_argument("--ca", default=DEFAULT_CA, help="trusted CA PEM")

    rate = parser.add_mutually_exclusive_group()
    rate.add_argument("--interval", type=parse_duration, default=1.0,
                      help="seconds between telemetry frames (default 1s)")
    rate.add_argument("--eps", type=float, default=None,
                      help="telemetry frames per second, e.g. 0.5 or 20; overrides --interval")
    parser.add_argument("--count", type=int, default=0, help="frames to publish, 0 means forever")
    parser.add_argument("--hold", type=parse_duration, default=0, help="keep listening after publishing")
    parser.add_argument("--payload", default=None,
                        help="custom JSON payload, supports ${i}, ${ts}, ${uuid}")
    parser.add_argument("--payload-file", default=None,
                        help="JSON file: object repeated or array cycled per frame")
    args = parser.parse_args(argv)
    if args.eps is not None:
        if args.eps <= 0:
            parser.error("--eps must be positive")
        args.interval = 1.0 / args.eps
    return args


if __name__ == "__main__":
    parsed = parse_args(sys.argv[1:])
    try:
        sys.exit(run(parsed))
    except (ConnectionError, TimeoutError) as error:
        # Do not guess a single cause: this handler fires for every transport
        # failure. Print the concrete error and point at the usual causes so
        # the operator can distinguish them instead of being told it is a
        # duplicate session when it is actually a bad secret or a registry
        # that was reached through an HTTP proxy.
        raise SystemExit(
            f"lost the broker connection: {error}\n"
            f"  client id: {parsed.client_id}\n"
            f"  device credential: {parsed.token}\n"
            "Common causes:\n"
            "  - wrong --token/--password (CONNACK 4 or 5)\n"
            "  - a second simulator with the same --client-id (default "
            "'nl-simulator') or the same token, which the presence layer "
            "fences; pass a unique --client-id\n"
            "  - the registry gRPC target routed through an HTTP proxy, which "
            "closes the HTTP/2 handshake; start the server without http_proxy "
            "or add the registry host to no_proxy"
        ) from None
