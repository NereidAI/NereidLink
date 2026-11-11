# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0

"""Shared device JWT signing helper for the NereidLink simulators.

JWT credentials (ADR-0009) are presented as the MQTT password or as the
password half of an HTTP Basic header. The platform accepts HS256, RS256
and ES256; this module signs with all three using PyJWT:

    - HS256: the key is a shared secret string.
    - RS256/ES256: the key is a PEM private key read from a file.

The issued token always carries iat/exp, plus nbf and iss/sub/aud when
requested, matching the constraints a tenant can pin on the credential.
"""

import time

import jwt

SUPPORTED_ALGORITHMS = ("HS256", "RS256", "ES256")


def load_signing_key(algorithm, secret=None, key_file=None):
    """Return the signing key material for the given algorithm.

    HS256 uses the literal secret; RS256/ES256 read a PEM private key
    from key_file. Exactly one source must be supplied.
    """
    if algorithm not in SUPPORTED_ALGORITHMS:
        raise ValueError(f"unsupported algorithm {algorithm}")
    if algorithm == "HS256":
        if not secret:
            raise ValueError("HS256 requires --jwt-secret")
        return secret
    if not key_file:
        raise ValueError(f"{algorithm} requires --jwt-key-file")
    with open(key_file, "r", encoding="utf-8") as handle:
        return handle.read()


def sign_device_jwt(
    key,
    algorithm="HS256",
    ttl_seconds=3600,
    issuer=None,
    subject=None,
    audience=None,
    not_before=0,
):
    """Sign one device JWT and return its compact serialization.

    The token carries iat and exp unconditionally (exp is required by the
    platform). nbf is added when not_before is non-zero, and iss/sub/aud
    when supplied so credential extra constraints can be matched.
    """
    now = int(time.time())
    payload = {"iat": now, "exp": now + int(ttl_seconds)}
    if not_before:
        payload["nbf"] = now + int(not_before)
    if issuer:
        payload["iss"] = issuer
    if subject:
        payload["sub"] = subject
    if audience:
        payload["aud"] = audience
    return jwt.encode(payload, key, algorithm=algorithm)
