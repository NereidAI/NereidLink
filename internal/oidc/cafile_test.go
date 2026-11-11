// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testSelfSignedPEM returns a minimal self-signed certificate PEM for the
// private-CA trust tests.
func testSelfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-oidc-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// writeTempPEM writes bytes into a temp file and returns its path.
func writeTempPEM(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write temp pem: %v", err)
	}
	return path
}
