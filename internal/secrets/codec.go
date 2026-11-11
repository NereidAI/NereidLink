// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package secrets implements the ADR-0026 `secret://` credential delivery.
// Connector desired config may embed the string "secret://<name>"; NL keeps
// only that reference in the spec and resolves it to plaintext at the MQTT
// downlink last mile. Values are sealed with AES-256-GCM under a
// key-encryption key (KEK) held in a file outside the database, so a database
// dump never exposes usable credentials. The package also walks a JSON
// document substituting every "secret://<name>" string for its resolved
// value; a missing secret resolves to an empty string and is counted so the
// relay can log it without failing delivery.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// keyFile is the default KEK file name inside the configured key directory.
const keyFile = "secret-encryption.key"

// ErrInvalidKeyMaterial marks a wrong-size KEK or corrupt sealed blob.
var ErrInvalidKeyMaterial = errors.New("invalid secret encryption material")

// Codec seals and opens secret values under a 32-byte AES-256 key.
type Codec struct {
	key []byte
}

// LoadCodec loads the KEK from dir, generating a new 32-byte key on first use
// exactly like the PKI device-key wrap. Losing the file makes previously
// sealed secrets undecryptable, so operators must back it up with the PKI
// directory.
func LoadCodec(dir string) (*Codec, error) {
	path := filepath.Join(dir, keyFile)
	raw, err := os.ReadFile(path) //nolint:gosec // operator-selected key directory
	if err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("%w: %s must be exactly 32 bytes", ErrInvalidKeyMaterial, keyFile)
		}
		return &Codec{key: raw}, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read secret encryption key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret encryption key: %w", err)
	}
	// O_CREATE|O_EXCL makes first-use generation safe across replicas sharing
	// the volume: exactly one process creates the KEK; a racing peer fails
	// with EEXIST and re-reads the winner's key instead of minting another.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: KEK path is operator-configured, not user input
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("write secret encryption key: %w", err)
		}
		return LoadCodec(dir)
	}
	if _, werr := f.Write(key); werr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write secret encryption key: %w", werr)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write secret encryption key: %w", err)
	}
	return &Codec{key: key}, nil
}

// NewCodec builds a codec from an explicit key (tests / injected KEK).
func NewCodec(key []byte) (*Codec, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: KEK must be exactly 32 bytes", ErrInvalidKeyMaterial)
	}
	cp := make([]byte, 32)
	copy(cp, key)
	return &Codec{key: cp}, nil
}

// Seal encrypts plaintext and returns base64(nonce||ciphertext).
func (c *Codec) Seal(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal.
func (c *Codec) Open(blob string) ([]byte, error) {
	sealed, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyMaterial, err)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrInvalidKeyMaterial
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
