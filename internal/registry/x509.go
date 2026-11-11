// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// x509.go maps a certificate presented at the mTLS layer to an X509
// device credential. Parsing and fingerprint rules live in
// internal/credverify so registry management paths and the authentication
// path cannot drift; the wrappers below preserve the local call sites.

package registry

import (
	"crypto/x509"

	"github.com/nereidai/nereidlink/internal/credverify"
)

// certFingerprintSHA256 returns the lowercase hex SHA-256 fingerprint of
// the DER-encoded certificate.
func certFingerprintSHA256(raw []byte) string {
	return credverify.CertificateFingerprint(raw)
}

// parseCredentialCertificate decodes the PEM certificate stored on an
// X509 credential.
func parseCredentialCertificate(pemCert string) (*x509.Certificate, error) {
	return credverify.ParseCertificate(pemCert)
}
