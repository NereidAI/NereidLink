// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// totp_test.go covers TOTP generation, validation and encrypted secrets.
package identity

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// TestGenerateTOTPSecret verifies secret shape and decodability.
func TestGenerateTOTPSecret(t *testing.T) {
	encoded, raw, err := generateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 20 {
		t.Fatalf("raw length = %d", len(raw))
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded)
	if err != nil {
		t.Fatalf("secret is not base32: %v", err)
	}
	if len(decoded) != 20 {
		t.Fatalf("decoded length = %d", len(decoded))
	}
}

// TestValidateTOTPCode verifies the generated current code validates.
func TestValidateTOTPCode(t *testing.T) {
	_, secret, err := generateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := totpCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !validateTOTPCode(secret, code, now) {
		t.Fatal("valid code rejected")
	}
	if validateTOTPCode(secret, "000000", now) {
		t.Fatal("invalid code accepted")
	}
}

// TestTOTPURI verifies provisioning parameters.
func TestTOTPURI(t *testing.T) {
	uri := totpURI("user@example.test", "SECRET")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatal(uri)
	}
	for _, wanted := range []string{"secret=SECRET", "issuer=NereidLink", "period=30"} {
		if !strings.Contains(uri, wanted) {
			t.Fatalf("URI %s missing %s", uri, wanted)
		}
	}
}

// TestMFASecretEncryption verifies encryption round trip and non-plaintext.
func TestMFASecretEncryption(t *testing.T) {
	svc := &Service{signer: &signer{secret: []byte("0123456789abcdef")}}
	plain := []byte("totp-secret")
	encoded, err := svc.encryptMFASecret(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, string(plain)) {
		t.Fatal("encrypted secret appears plaintext")
	}
	decoded, err := svc.decryptMFASecret(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(plain) {
		t.Fatalf("decoded = %q", decoded)
	}
}

// TestGenerateRecoveryCodes verifies unique codes and hashes.
func TestGenerateRecoveryCodes(t *testing.T) {
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(hashes) != recoveryCodeCount {
		t.Fatalf("codes=%d hashes=%d", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for i, code := range codes {
		if len(code) != recoveryCodeLength || seen[code] {
			t.Fatalf("invalid or duplicate code %q", code)
		}
		seen[code] = true
		if hashes[i] == code {
			t.Fatal("recovery code stored plaintext")
		}
	}
}
