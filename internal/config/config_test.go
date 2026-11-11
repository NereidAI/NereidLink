// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// config_test.go covers the internal-link TLS section: validation rules and
// the hardened TLS 1.3 profile built for the NATS link.

package config

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

// TestLinkTLSDisabledRejectsSettings ensures TLS-only fields cannot be set
// while the link is plaintext.
func TestLinkTLSDisabledRejectsSettings(t *testing.T) {
	cases := []LinkTLSSection{
		{CAFile: "ca.pem"},
		{CertFile: "c.pem", KeyFile: "k.pem"},
		{InsecureSkipVerify: true},
	}
	for i, c := range cases {
		if err := c.Validate("nats.tls"); err == nil {
			t.Fatalf("case %d: expected error for TLS settings while disabled", i)
		}
	}
	if err := (LinkTLSSection{}).Validate("nats.tls"); err != nil {
		t.Fatalf("disabled empty section should be valid: %v", err)
	}
}

// TestLinkTLSValidation covers pairing and verification rules.
func TestLinkTLSValidation(t *testing.T) {
	if err := (LinkTLSSection{Enabled: true, CertFile: "c.pem"}).Validate("nats.tls"); err == nil {
		t.Fatal("cert without key must fail")
	}
	if err := (LinkTLSSection{Enabled: true, InsecureSkipVerify: true, CAFile: "ca.pem"}).Validate("nats.tls"); err == nil {
		t.Fatal("insecure + ca must fail")
	}
	if err := (LinkTLSSection{Enabled: true}).Validate("nats.tls"); err != nil {
		t.Fatalf("enabled with no material should be valid (system roots): %v", err)
	}
}

// TestLinkTLSConfigProfile asserts TLS 1.3 and CA/client material loading.
func TestLinkTLSConfigProfile(t *testing.T) {
	dir := t.TempDir()
	caPEM, certPEM, keyPEM := writeTestPKI(t, dir)

	cfg, err := LinkTLSSection{Enabled: true, CAFile: caPEM, CertFile: certPEM, KeyFile: keyPEM}.TLSConfig()
	if err != nil {
		t.Fatalf("build tls config: %v", err)
	}
	if cfg.MinVersion != tlsVersion13 {
		t.Fatalf("expected TLS 1.3 minimum, got %d", cfg.MinVersion)
	}
	if cfg.RootCAs == nil || len(cfg.Certificates) != 1 {
		t.Fatal("expected CA pool and one client certificate")
	}

	// Disabled yields nil so callers stay plaintext.
	off, err := (LinkTLSSection{}).TLSConfig()
	if err != nil || off != nil {
		t.Fatalf("disabled TLSConfig must be nil/nil, got %v/%v", off, err)
	}
}

// TestLinkServerTLSConfig asserts the listener requires a server
// certificate and upgrades to mandatory client verification with a CA.
func TestLinkServerTLSConfig(t *testing.T) {
	dir := t.TempDir()
	caPEM, certPEM, keyPEM := writeTestPKI(t, dir)

	if _, err := (LinkTLSSection{Enabled: true}).ServerTLSConfig(); err == nil {
		t.Fatal("server TLS without a certificate must fail")
	}
	cfg, err := LinkTLSSection{Enabled: true, CAFile: caPEM, CertFile: certPEM, KeyFile: keyPEM}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("server tls config: %v", err)
	}
	if cfg.MinVersion != tlsVersion13 {
		t.Fatalf("expected TLS 1.3 minimum, got %d", cfg.MinVersion)
	}
	if cfg.ClientAuth != clientAuthRequireAndVerify {
		t.Fatalf("expected mTLS required, got %v", cfg.ClientAuth)
	}
	off, err := (LinkTLSSection{}).ServerTLSConfig()
	if err != nil || off != nil {
		t.Fatalf("disabled server TLSConfig must be nil/nil, got %v/%v", off, err)
	}
}

// TestAccountingValidation covers the ADR-0025 resource accounting bounds.
func TestAccountingValidation(t *testing.T) {
	// Disabled accounting accepts anything.
	c := Config{Accounting: AccountingSection{Enabled: false, SampleRate: 0}}
	if err := c.validateAccounting(); err != nil {
		t.Fatalf("disabled accounting must validate: %v", err)
	}
	// sample_rate must be at least 1.
	c = Config{Accounting: AccountingSection{Enabled: true, SampleRate: 0, Capacity: 8, TopN: 4}}
	if c.validateAccounting() == nil {
		t.Fatal("sample_rate 0 must fail")
	}
	// capacity out of range fails.
	c.Accounting.SampleRate = 1
	c.Accounting.Capacity = 0
	if c.validateAccounting() == nil {
		t.Fatal("capacity 0 must fail")
	}
	c.Accounting.Capacity = 100000
	if c.validateAccounting() == nil {
		t.Fatal("oversized capacity must fail")
	}
	// top_n above capacity fails.
	c.Accounting.Capacity = 16
	c.Accounting.TopN = 32
	if c.validateAccounting() == nil {
		t.Fatal("top_n above capacity must fail")
	}
	// A valid configuration passes.
	c.Accounting.TopN = 8
	if err := c.validateAccounting(); err != nil {
		t.Fatalf("valid accounting config rejected: %v", err)
	}
}

// clientAuthRequireAndVerify mirrors tls.RequireAndVerifyClientCert.
const clientAuthRequireAndVerify = 4

// TestDeadmanValidation covers the independent fallback channel rules.
func TestDeadmanValidation(t *testing.T) {
	// Enabled with no endpoint is rejected.
	c := Config{Deadman: DeadmanSection{Enabled: true}}
	if c.validateDeadman() == nil {
		t.Fatal("enabled deadman without endpoint must fail")
	}
	// Enabled sharing the notifier webhook is rejected.
	c = Config{
		Notify:  NotifySection{WebhookURL: "https://hooks.example/x"},
		Deadman: DeadmanSection{Enabled: true, WebhookURL: "https://hooks.example/x"},
	}
	if c.validateDeadman() == nil {
		t.Fatal("deadman webhook must differ from notify webhook")
	}
	// A distinct webhook is accepted.
	c.Deadman.WebhookURL = "https://hooks.example/deadman"
	if err := c.validateDeadman(); err != nil {
		t.Fatalf("distinct deadman webhook should be valid: %v", err)
	}
}

// TestCredentialCacheValidation covers the default-off switch and bounds of
// the optional authentication cache.
func TestCredentialCacheValidation(t *testing.T) {
	c := Config{CredentialCache: CredentialCacheSection{
		Enabled:     false,
		PositiveTTL: "not-a-duration",
		MaxCost:     -1,
	}}
	if err := c.validateCredentialCache(); err != nil {
		t.Fatalf("disabled credential cache must ignore other settings: %v", err)
	}

	c = Config{CredentialCache: CredentialCacheSection{Enabled: true, PositiveTTL: "0s"}}
	if c.validateCredentialCache() == nil {
		t.Fatal("non-positive positive TTL must fail")
	}
	c.CredentialCache = CredentialCacheSection{Enabled: true, NegativeTTL: "bad"}
	if c.validateCredentialCache() == nil {
		t.Fatal("invalid negative TTL must fail")
	}
	c.CredentialCache = CredentialCacheSection{Enabled: true, NumCounters: -1}
	if c.validateCredentialCache() == nil {
		t.Fatal("negative num counters must fail")
	}
	c.CredentialCache = CredentialCacheSection{
		Enabled:     true,
		PositiveTTL: "45s",
		NegativeTTL: "3s",
	}
	if err := c.validateCredentialCache(); err != nil {
		t.Fatalf("valid credential cache rejected: %v", err)
	}
}

// tlsVersion13 mirrors tls.VersionTLS13 without importing the package twice.
const tlsVersion13 = 0x0304

// writeTestPKI emits a self-signed CA and a leaf keypair, returning their
// paths, for exercising certificate loading.
func writeTestPKI(t *testing.T, dir string) (caFile, certFile, keyFile string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "nereidlink-link"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caTmpl, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	caFile = filepath.Join(dir, "ca.pem")
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	writePEM(t, caFile, "CERTIFICATE", caDER)
	writePEM(t, certFile, "CERTIFICATE", leafDER)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	return caFile, certFile, keyFile
}

// writePEM writes one PEM block to path.
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}
