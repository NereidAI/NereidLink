// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// revocation_test.go covers CRL-based revocation: hit/miss by serial, TTL
// caching, negative caching, stale-list tolerance and DER/PEM parsing.

package registry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"
)

// testCA builds a self-signed CA certificate usable as a CRL issuer.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1000),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          []byte{1, 2, 3, 4},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	return ca, key
}

// testCRL builds a DER-encoded CRL revoking the given serials.
func testCRL(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, serials ...*big.Int) []byte {
	t.Helper()
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(1),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                time.Now().Add(time.Hour),
		RevokedCertificateEntries: make([]x509.RevocationListEntry, 0, len(serials)),
	}
	for _, s := range serials {
		tmpl.RevokedCertificateEntries = append(tmpl.RevokedCertificateEntries,
			x509.RevocationListEntry{SerialNumber: s, RevocationTime: time.Now()})
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca, key)
	if err != nil {
		t.Fatalf("create crl: %v", err)
	}
	return der
}

// testCert builds a minimal certificate with the given serial and an
// optional CRL distribution point.
func testCert(t *testing.T, serial *big.Int, dp string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if dp != "" {
		tmpl.CRLDistributionPoints = []string{dp}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// TestRevocationHitAndMiss covers serial matching and the no-source case.
func TestRevocationHitAndMiss(t *testing.T) {
	ca, caKey := testCA(t)
	revokedSerial := big.NewInt(42)
	liveSerial := big.NewInt(7)
	crl := testCRL(t, ca, caKey, revokedSerial)

	r := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	r.fetch = func(context.Context, string) ([]byte, error) { return crl, nil }

	// Certificate without a distribution point and no override: not revoked.
	noSource := testCert(t, revokedSerial, "")
	if revoked, err := r.Revoked(context.Background(), noSource, ""); err != nil || revoked {
		t.Fatalf("no CRL source must not be revoked: revoked=%v err=%v", revoked, err)
	}
	// Override URL points at the fetched CRL.
	if revoked, err := r.Revoked(context.Background(), testCert(t, revokedSerial, ""), "https://ca/crl"); err != nil || !revoked {
		t.Fatalf("revoked serial must be detected: revoked=%v err=%v", revoked, err)
	}
	if revoked, err := r.Revoked(context.Background(), testCert(t, liveSerial, ""), "https://ca/crl"); err != nil || revoked {
		t.Fatalf("live serial must not be revoked: revoked=%v err=%v", revoked, err)
	}
}

// TestRevocationUsesDistributionPoint confirms the cert DP is used when no
// override is supplied.
func TestRevocationUsesDistributionPoint(t *testing.T) {
	ca, caKey := testCA(t)
	revokedSerial := big.NewInt(99)
	crl := testCRL(t, ca, caKey, revokedSerial)
	var gotURL string
	r := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	r.fetch = func(_ context.Context, url string) ([]byte, error) {
		gotURL = url
		return crl, nil
	}
	cert := testCert(t, revokedSerial, "https://ca/from-dp.crl")
	revoked, err := r.Revoked(context.Background(), cert, "")
	if err != nil || !revoked {
		t.Fatalf("dp-based revocation must be detected: revoked=%v err=%v", revoked, err)
	}
	if gotURL != "https://ca/from-dp.crl" {
		t.Fatalf("expected DP url, got %q", gotURL)
	}
}

// TestRevocationCacheTTL ensures one fetch per TTL window.
func TestRevocationCacheTTL(t *testing.T) {
	ca, caKey := testCA(t)
	crl := testCRL(t, ca, caKey)
	var calls int32
	now := time.Unix(1_700_000_000, 0)
	r := &crlResolver{cache: map[string]*crlEntry{}, now: func() time.Time { return now }}
	r.fetch = func(context.Context, string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return crl, nil
	}
	cert := testCert(t, big.NewInt(1), "https://ca/crl")
	for i := 0; i < 3; i++ {
		if _, err := r.Revoked(context.Background(), cert, ""); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", got)
	}
	now = now.Add(crlCacheTTL + time.Second)
	if _, err := r.Revoked(context.Background(), cert, ""); err != nil {
		t.Fatalf("post-TTL check: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected refetch after TTL, got %d", got)
	}
}

// TestRevocationFailClosedAndStaleTolerance covers fetch failure: a fresh
// failure errors (caller fails closed), while a previously cached list is
// served across a transient outage.
func TestRevocationFailClosedAndStaleTolerance(t *testing.T) {
	ca, caKey := testCA(t)
	crl := testCRL(t, ca, caKey, big.NewInt(5))
	now := time.Unix(1_700_000_000, 0)
	var down int32
	r := &crlResolver{cache: map[string]*crlEntry{}, now: func() time.Time { return now }}
	r.fetch = func(context.Context, string) ([]byte, error) {
		if atomic.LoadInt32(&down) == 1 {
			return nil, fmt.Errorf("unreachable")
		}
		return crl, nil
	}
	cert := testCert(t, big.NewInt(5), "https://ca/crl")

	// Prime the cache.
	revoked, err := r.Revoked(context.Background(), cert, "")
	if err != nil || !revoked {
		t.Fatalf("prime: revoked=%v err=%v", revoked, err)
	}
	// Expire and go down: stale list still answers.
	now = now.Add(crlCacheTTL + time.Second)
	atomic.StoreInt32(&down, 1)
	if revoked, err := r.Revoked(context.Background(), cert, ""); err != nil || !revoked {
		t.Fatalf("stale list must survive outage: revoked=%v err=%v", revoked, err)
	}
	// A never-seen URL while down must error (fail closed).
	if _, err := r.Revoked(context.Background(), testCert(t, big.NewInt(5), ""), "https://other/crl"); err == nil {
		t.Fatal("unreachable new CRL must error")
	}
}

// TestParseCRLPEM accepts PEM-wrapped CRLs.
func TestParseCRLPEM(t *testing.T) {
	ca, caKey := testCA(t)
	der := testCRL(t, ca, caKey)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	list, err := parseCRL(pemBytes)
	if err != nil || list == nil {
		t.Fatalf("parse pem crl: %v", err)
	}
}
