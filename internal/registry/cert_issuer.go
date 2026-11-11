// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// cert_issuer.go defines the seam between the registry and the private
// PKI for X509 credentials. The registry stays independent of the PKI
// package: it only asks for in-memory issuance and for private-key
// wrapping so issued keys can be stored and downloaded later.

package registry

import (
	"context"
	"time"
)

// CertificateIssuer is implemented by *pki.Manager.
type CertificateIssuer interface {
	// IssueDevice generates a key, signs a client certificate with the
	// built-in CA and returns certificate, private key and CA PEM.
	IssueDevice(ctx context.Context, cn string, notAfter time.Time) (certPEM, keyPEM, caPEM []byte, err error)

	// EncryptPrivateKey seals a private key PEM for extra storage.
	EncryptPrivateKey(plaintext []byte) (string, error)

	// DecryptPrivateKey opens a sealed private key PEM.
	DecryptPrivateKey(blob string) ([]byte, error)
}

// SetCertificateIssuer wires the PKI issuer.
func (a *Admin) SetCertificateIssuer(issuer CertificateIssuer) {
	a.certIssuer = issuer
}
