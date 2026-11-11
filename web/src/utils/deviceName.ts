// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// deviceName generates the canonical technical name for a console-created
// device. The name is produced client-side only: the platform API still
// accepts any caller-supplied canonical_name and enforces uniqueness with
// its UNIQUE(tenant_id, canonical_name) constraint, so third-party
// integrations are free to choose their own scheme. The console generates
// names so operators never have to invent unique technical identifiers.
//
// Format: "<prefix>-<8 chars>" where the 8 chars alternate lowercase
// letter / digit: letter-digit-letter-digit-letter-digit-letter-digit
// (e.g. dev-a3f9k2x7, gw-k2x7a3f9). The key space is 26^4 * 10^4 ~=
// 4.57e9; the create form regenerates on the rare 409 collision.

// RANDOM_SUFFIX_LEN is the number of alternating characters.
const RANDOM_SUFFIX_LEN = 8;

// randomIndex returns an unbiased integer in [0, range) from
// crypto.getRandomValues using rejection sampling, so the tiny modulo
// bias never concentrates collisions on some letters/digits.
function randomIndex(range: number): number {
  const maxValid = Math.floor(256 / range) * range;
  const buf = new Uint8Array(1);
  for (;;) {
    crypto.getRandomValues(buf);
    if (buf[0] < maxValid) return buf[0] % range;
  }
}

// randomSuffix builds the alternating letter/digit tail.
function randomSuffix(): string {
  let out = "";
  for (let i = 0; i < RANDOM_SUFFIX_LEN; i += 1) {
    if (i % 2 === 0) {
      out += String.fromCharCode(97 + randomIndex(26)); // a-z
    } else {
      out += String.fromCharCode(48 + randomIndex(10)); // 0-9
    }
  }
  return out;
}

// generateDeviceName returns a fresh technical canonical name.
// isGateway selects the gw- prefix; ordinary and sub-devices use dev-.
export function generateDeviceName(isGateway: boolean): string {
  return `${isGateway ? "gw" : "dev"}-${randomSuffix()}`;
}

// credentialSuffix maps a credential type to the conventional suffix used
// for the auto-derived credentials_id.
const credentialSuffix: Record<string, string> = {
  TOKEN: "token",
  PASSWORD: "password",
  X509: "cert",
  JWT: "jwt",
};

// deriveCredentialId builds the default credentials_id for a device and
// credential type: "<lowercased canonical name>_<suffix>", e.g.
// dev-a3f9k2x7_token. The operator can still edit the field.
export function deriveCredentialId(
  canonicalName: string,
  credentialType: string,
): string {
  const suffix = credentialSuffix[credentialType] ?? "credential";
  return `${canonicalName.trim().toLowerCase()}_${suffix}`;
}
