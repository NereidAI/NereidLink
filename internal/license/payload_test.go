// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// payload_test.go verifies signed license parsing and rejection paths.

package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDecodeVerifiesSignedPayload(t *testing.T) {
	public, private := mustTestKey(t)
	payload := Payload{
		Scope:        "00000000-0000-0000-0000-000000000001",
		TenantID:     "00000000-0000-0000-0000-000000000001",
		CustomerName: "Test Tenant", DeviceCap: 10,
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Features:  []string{},
	}
	file := mustSign(t, payload, private)
	decoded, err := Decode(file, public)
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if decoded.CustomerName != payload.CustomerName {
		t.Fatalf("customer = %q, want %q", decoded.CustomerName, payload.CustomerName)
	}
}

func TestDecodeRejectsTamperedSignature(t *testing.T) {
	public, private := mustTestKey(t)
	file := mustSign(t, Payload{
		Scope: "*", CustomerName: "Platform", DeviceCap: 100, TenantCap: 1,
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour),
		Features:  []string{},
	}, private)
	file.Signature[0] ^= 1
	if _, err := Decode(file, public); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidSignature)
	}
}

func TestPayloadValidateRejectsMismatchedTenantScope(t *testing.T) {
	payload := Payload{
		Scope:     "00000000-0000-0000-0000-000000000001",
		TenantID:  "00000000-0000-0000-0000-000000000002",
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour),
		Features:  []string{},
	}
	if err := payload.Validate(); !errors.Is(err, ErrInvalidLicense) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidLicense)
	}
}

func mustTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return public, private
}

func mustSign(t *testing.T, payload Payload, key ed25519.PrivateKey) SignedFile {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return SignedFile{Payload: data, Signature: ed25519.Sign(key, data)}
}
