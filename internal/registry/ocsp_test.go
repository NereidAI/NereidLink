// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ocsp_test.go covers OCSP revocation: conclusive Good/Revoked answers
// short-circuit the CRL fallback, Unknown or an unreachable responder
// falls back to CRL, and positive/negative answers are TTL cached.

package registry

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// testLeaf builds a leaf signed by ca with an optional OCSP responder
// URL and CRL distribution point.
func testLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial *big.Int, ocspURL, crlURL string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:   serial,
		Subject:        pkix.Name{CommonName: "device"},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(time.Hour),
		AuthorityKeyId: ca.SubjectKeyId,
	}
	if ocspURL != "" {
		tmpl.OCSPServer = []string{ocspURL}
	}
	if crlURL != "" {
		tmpl.CRLDistributionPoints = []string{crlURL}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert
}

// ocspTestServer stands up an OCSP responder signed directly by ca. The
// statusBySerial map selects Good/Revoked/Unknown; a serial absent from
// the map yields Unknown. It returns the server and a hit counter.
func ocspTestServer(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, statusBySerial map[string]int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		v, known := statusBySerial[req.SerialNumber.String()]
		// An unmapped serial means Unknown; the int zero value is Good,
		// so it must be set explicitly.
		status := ocsp.Unknown
		if known {
			status = v
		}
		template := ocsp.Response{
			Status:       status,
			SerialNumber: req.SerialNumber,
			ThisUpdate:   time.Now(),
			NextUpdate:   time.Now().Add(time.Hour),
		}
		if status == ocsp.Revoked {
			template.RevokedAt = time.Now()
			template.RevocationReason = ocsp.Unspecified
		}
		der, err := ocsp.CreateResponse(ca, ca, template, caKey)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(der)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// postTo forwards one real DER OCSP request to url via a plain client.
func postTo(url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ocsp-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("ocsp status " + http.StatusText(resp.StatusCode))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// crlMustNotBeCalled returns a CRL fetcher that fails the test, used to
// prove a conclusive OCSP answer short-circuits the CRL fallback.
func crlMustNotBeCalled(t *testing.T) func(context.Context, string) ([]byte, error) {
	t.Helper()
	return func(context.Context, string) ([]byte, error) {
		t.Fatal("CRL must not be fetched when OCSP answers conclusively")
		return nil, nil
	}
}

// TestOCSPGoodShortCircuits verifies a Good answer is not revoked and
// never consults CRL.
func TestOCSPGoodShortCircuits(t *testing.T) {
	ca, caKey := testCA(t)
	srv, _ := ocspTestServer(t, ca, caKey, map[string]int{
		big.NewInt(1).String(): ocsp.Good,
	})
	leaf := testLeaf(t, ca, caKey, big.NewInt(1), srv.URL, "")

	c := &revocationChecker{ocsp: newOCSPResolver()}
	crl := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	crl.fetch = crlMustNotBeCalled(t)
	c.crl = crl

	if revoked, err := c.Revoked(context.Background(), leaf, ca, ""); err != nil || revoked {
		t.Fatalf("good OCSP: revoked=%v err=%v", revoked, err)
	}
}

// TestOCSPRevoked verifies a Revoked answer rejects the certificate and
// never consults CRL.
func TestOCSPRevoked(t *testing.T) {
	ca, caKey := testCA(t)
	srv, _ := ocspTestServer(t, ca, caKey, map[string]int{
		big.NewInt(2).String(): ocsp.Revoked,
	})
	leaf := testLeaf(t, ca, caKey, big.NewInt(2), srv.URL, "")

	c := &revocationChecker{ocsp: newOCSPResolver()}
	crl := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	crl.fetch = crlMustNotBeCalled(t)
	c.crl = crl

	if revoked, err := c.Revoked(context.Background(), leaf, ca, ""); err != nil || !revoked {
		t.Fatalf("revoked OCSP: revoked=%v err=%v", revoked, err)
	}
}

// TestOCSPUnknownFallsBackToCRL verifies an explicit Unknown answer
// defers revocation to the CRL.
func TestOCSPUnknownFallsBackToCRL(t *testing.T) {
	ca, caKey := testCA(t)
	srv, _ := ocspTestServer(t, ca, caKey, map[string]int{}) // absent serial -> Unknown
	leaf := testLeaf(t, ca, caKey, big.NewInt(3), srv.URL, "https://ca/crl")
	crlBytes := testCRL(t, ca, caKey, big.NewInt(3))

	c := &revocationChecker{ocsp: newOCSPResolver()}
	crl := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	crl.fetch = func(context.Context, string) ([]byte, error) { return crlBytes, nil }
	c.crl = crl

	if revoked, err := c.Revoked(context.Background(), leaf, ca, ""); err != nil || !revoked {
		t.Fatalf("unknown OCSP must fall through to CRL: revoked=%v err=%v", revoked, err)
	}
}

// TestOCSPResponderDownFallsBackToCRL verifies an unreachable responder
// does not fail open by itself: the CRL still gets the final say.
func TestOCSPResponderDownFallsBackToCRL(t *testing.T) {
	ca, caKey := testCA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // closed on purpose: connection refused
	leaf := testLeaf(t, ca, caKey, big.NewInt(4), srv.URL, "https://ca/crl")
	crlBytes := testCRL(t, ca, caKey, big.NewInt(4))

	c := &revocationChecker{ocsp: newOCSPResolver()}
	crl := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
	crl.fetch = func(context.Context, string) ([]byte, error) { return crlBytes, nil }
	c.crl = crl

	if revoked, err := c.Revoked(context.Background(), leaf, ca, ""); err != nil || !revoked {
		t.Fatalf("down OCSP must fall through to CRL: revoked=%v err=%v", revoked, err)
	}
}

// TestOCSPNoResponderOrIssuerSkipsToCRL covers a cert without an AIA
// responder and a nil issuer.
func TestOCSPNoResponderOrIssuerSkipsToCRL(t *testing.T) {
	ca, caKey := testCA(t)
	leaf := testLeaf(t, ca, caKey, big.NewInt(5), "", "https://ca/crl")
	crlBytes := testCRL(t, ca, caKey, big.NewInt(5))

	newChecker := func() *revocationChecker {
		c := &revocationChecker{ocsp: newOCSPResolver()}
		crl := &crlResolver{cache: map[string]*crlEntry{}, now: time.Now}
		crl.fetch = func(context.Context, string) ([]byte, error) { return crlBytes, nil }
		c.crl = crl
		return c
	}

	// No AIA responder on the cert: CRL decides.
	if revoked, err := newChecker().Revoked(context.Background(), leaf, ca, ""); err != nil || !revoked {
		t.Fatalf("no-responder cert must use CRL: revoked=%v err=%v", revoked, err)
	}
	// Nil issuer: OCSP cannot run, CRL decides.
	if revoked, err := newChecker().Revoked(context.Background(), leaf, nil, ""); err != nil || !revoked {
		t.Fatalf("nil issuer must use CRL: revoked=%v err=%v", revoked, err)
	}
}

// TestOCSPPositiveCacheTTL verifies one responder call per TTL window.
func TestOCSPPositiveCacheTTL(t *testing.T) {
	ca, caKey := testCA(t)
	srv, hits := ocspTestServer(t, ca, caKey, map[string]int{
		big.NewInt(6).String(): ocsp.Good,
	})
	now := time.Unix(1_700_000_000, 0)
	o := &ocspResolver{cache: map[string]*ocspEntry{}, now: func() time.Time { return now }}
	o.fetch = func(_ context.Context, _ string, body []byte) ([]byte, error) {
		return postTo(srv.URL, body)
	}
	leaf := testLeaf(t, ca, caKey, big.NewInt(6), srv.URL, "")

	for i := 0; i < 3; i++ {
		if status, err := o.status(context.Background(), leaf, ca); err != nil || status != ocsp.Good {
			t.Fatalf("check %d: status=%d err=%v", i, status, err)
		}
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("expected 1 responder call within TTL, got %d", got)
	}
	now = now.Add(ocspCacheTTL + time.Second)
	if _, err := o.status(context.Background(), leaf, ca); err != nil {
		t.Fatalf("post-TTL check: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("expected refetch after TTL, got %d", got)
	}
}

// TestOCSPNegativeCache verifies failures are throttled within the
// negative TTL and retried after it.
func TestOCSPNegativeCache(t *testing.T) {
	ca, caKey := testCA(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	o := &ocspResolver{cache: map[string]*ocspEntry{}, now: func() time.Time { return now }}
	o.fetch = func(_ context.Context, _ string, body []byte) ([]byte, error) {
		return postTo(srv.URL, body)
	}
	leaf := testLeaf(t, ca, caKey, big.NewInt(7), srv.URL, "")

	if _, err := o.status(context.Background(), leaf, ca); err == nil {
		t.Fatal("failing responder must error")
	}
	if _, err := o.status(context.Background(), leaf, ca); !errors.Is(err, errOCSPThrottled) {
		t.Fatalf("second call within negative window must be throttled, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 call within negative window, got %d", got)
	}
	now = now.Add(ocspNegativeTTL + time.Second)
	if _, err := o.status(context.Background(), leaf, ca); err == nil {
		t.Fatal("still-failing responder must error after window too")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected retry after negative window, got %d", got)
	}
}
