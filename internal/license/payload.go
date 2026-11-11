// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// payload.go defines the signed license file format and Ed25519 key
// loading. The payload is transported as opaque bytes so signature
// verification never depends on JSON field ordering.

package license

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// Sentinel errors for invalid license files and policy decisions.
var (
	// ErrInvalidSignature means the Ed25519 signature did not verify.
	ErrInvalidSignature = errors.New("invalid license signature")

	// ErrInvalidLicense means the signed payload failed validation.
	ErrInvalidLicense = errors.New("invalid license payload")

	// ErrReadOnly means the license permits queries but not new resources.
	ErrReadOnly = errors.New("license is read-only")

	// ErrSuspended means the tenant was manually suspended.
	ErrSuspended = errors.New("tenant is suspended")
)

// Payload is the canonical signed license body.
type Payload struct {
	// Scope is '*' for a deployment license or a tenant UUID.
	Scope string `json:"scope"`

	// TenantID mirrors Scope for tenant licenses and is empty for '*'.
	TenantID string `json:"tenant_id,omitempty"`

	// CustomerName is the contract display name.
	CustomerName string `json:"customer_name"`

	// DeviceCap limits registered devices; zero means unlimited.
	DeviceCap int64 `json:"device_cap"`

	// TenantCap limits tenant count on a deployment license; zero means unlimited.
	TenantCap int64 `json:"tenant_cap,omitempty"`

	// IssuedAt is the vendor issuance time.
	IssuedAt time.Time `json:"issued_at"`

	// ExpiresAt is the contract end time.
	ExpiresAt time.Time `json:"expires_at"`

	// Features lists purchased feature identifiers.
	Features []string `json:"features"`
}

// File is the envelope read by the operations CLI.
type File struct {
	// Payload is the base64-encoded JSON payload.
	Payload string `json:"payload"`

	// Signature is the base64-encoded Ed25519 signature over Payload.
	Signature string `json:"signature"`
}

// SignedFile groups the exact bytes that were signed with their signature.
type SignedFile struct {
	// Payload is the exact JSON byte sequence.
	Payload []byte

	// Signature is the detached Ed25519 signature.
	Signature []byte
}

// LoadPublicKey reads an Ed25519 public key in PEM SubjectPublicKeyInfo form.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	//nolint:gosec // operator-selected license key path
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%w: expected PUBLIC KEY PEM", ErrInvalidLicense)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse license public key: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: key is not Ed25519", ErrInvalidLicense)
	}
	return key, nil
}

// ReadFile parses a JSON envelope from path.
func ReadFile(path string) (SignedFile, error) {
	//nolint:gosec // operator-selected license file path
	data, err := os.ReadFile(path)
	if err != nil {
		return SignedFile{}, err
	}
	return ParseFile(data)
}

// ParseFile parses a JSON envelope so both the install CLI and the
// ADR-0023 license.replace executor validate one envelope format.
func ParseFile(data []byte) (SignedFile, error) {
	var envelope File
	if err := json.Unmarshal(data, &envelope); err != nil {
		return SignedFile{}, fmt.Errorf("%w: %v", ErrInvalidLicense, err)
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return SignedFile{}, fmt.Errorf("%w: payload is not base64", ErrInvalidLicense)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return SignedFile{}, fmt.Errorf("%w: signature is not base64", ErrInvalidLicense)
	}
	return SignedFile{Payload: payload, Signature: signature}, nil
}

// Decode verifies the detached signature, parses and validates payload.
func Decode(file SignedFile, key ed25519.PublicKey) (Payload, error) {
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, file.Payload, file.Signature) {
		return Payload{}, ErrInvalidSignature
	}
	var payload Payload
	if err := json.Unmarshal(file.Payload, &payload); err != nil {
		return Payload{}, fmt.Errorf("%w: %v", ErrInvalidLicense, err)
	}
	if err := payload.Validate(); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

// Validate checks the invariant shape required before installation.
func (p Payload) Validate() error {
	if p.Scope != "*" && p.Scope != p.TenantID {
		return fmt.Errorf("%w: tenant scope and tenant_id differ", ErrInvalidLicense)
	}
	if p.Scope == "" || p.DeviceCap < 0 || p.TenantCap < 0 {
		return fmt.Errorf("%w: scope and caps must be valid", ErrInvalidLicense)
	}
	if p.IssuedAt.IsZero() || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(p.IssuedAt) {
		return fmt.Errorf("%w: invalid validity window", ErrInvalidLicense)
	}
	if p.Features == nil {
		return fmt.Errorf("%w: features must be an array", ErrInvalidLicense)
	}
	return nil
}
