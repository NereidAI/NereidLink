// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// cert.go maps a leaf certificate presented at the mTLS layer to an X509
// credential. The TLS listener already verifies the chain against the
// trusted CA pool; this layer proves that the exact leaf certificate is the
// one registered for the device.

package credverify

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
)

// errInvalidCertificate marks an unparseable certificate stored on an
// X509 credential.
var errInvalidCertificate = errors.New("invalid registered device certificate")

// CertificateFingerprint returns the lowercase hex SHA-256 fingerprint of
// the DER-encoded certificate.
func CertificateFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ParseCertificate decodes the PEM certificate stored on an X509
// credential. It returns an error when the value is not exactly one
// parseable certificate; malformed stored material must never authenticate.
func ParseCertificate(pemCert string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemCert))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errInvalidCertificate
	}
	return x509.ParseCertificate(block.Bytes)
}

// verifyCertificate reports whether presentedFingerprint matches the
// SHA-256 fingerprint of the registered certificate. Comparison is
// constant time on the normalized hex strings.
func verifyCertificate(storedPEM, presentedFingerprint string) (bool, error) {
	if presentedFingerprint == "" {
		return false, nil
	}
	cert, err := ParseCertificate(storedPEM)
	if err != nil {
		return false, err
	}
	expected := CertificateFingerprint(cert.Raw)
	return subtle.ConstantTimeCompare(
		[]byte(expected), []byte(strings.ToLower(presentedFingerprint))) == 1, nil
}
