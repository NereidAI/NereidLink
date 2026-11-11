// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// keywrap.go encrypts device private keys at rest with AES-256-GCM.
// UI-issued device private keys are stored encrypted inside the
// credential extra JSONB so they stay downloadable while a database
// copy never exposes a usable identity. The wrapping key lives only in
// the PKI directory, never in the database.

package pki

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

// keyEncryptionKeyFile is the PKI file holding the 32-byte AES key.
const keyEncryptionKeyFile = "device-key-encryption.key"

// errInvalidKeyMaterial marks a wrong-size wrapping key or bad blob.
var errInvalidKeyMaterial = errors.New("invalid key encryption material")

// loadOrCreateKeyEncryptionKey returns the 32-byte wrapping key from
// dir, generating it on first use. The file is written 0600 alongside
// the CA key; losing it makes previously stored private keys
// undecryptable, so operators must back it up with the CA directory.
func loadOrCreateKeyEncryptionKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, keyEncryptionKeyFile)
	raw, err := os.ReadFile(path) //nolint:gosec // operator-selected PKI directory
	if err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("%w: %s must be exactly 32 bytes", errInvalidKeyMaterial, keyEncryptionKeyFile)
		}
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key encryption key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key encryption key: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write key encryption key: %w", err)
	}
	return key, nil
}

// encryptPrivateKey seals a PEM private key and returns a base64 blob
// with the random GCM nonce as a prefix.
func encryptPrivateKey(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
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

// decryptPrivateKey reverses encryptPrivateKey.
func decryptPrivateKey(key []byte, blob string) ([]byte, error) {
	sealed, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidKeyMaterial, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errInvalidKeyMaterial
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
