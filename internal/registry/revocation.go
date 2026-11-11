// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// revocation.go decides whether an X.509 device certificate is revoked,
// per ADR-0009. Certificate chain validation already happens at the mTLS
// layer; this layer adds the revocation decision the chain check omits.
//
// OCSP is checked first (fresher revocation data); a conclusive Good or
// Revoked answer short-circuits, while no responder or an unavailable one
// falls back to the issuer Certificate Revocation List. Because the
// connection hot path must not depend on either external endpoint, both
// answers are cached in-process for a short TTL (no Redis) and an
// unreachable or unparseable source errors out so the mTLS hook fails
// closed. The CRL URL comes either from the credential extra (`crl_url`)
// or the certificate's own CRL Distribution Points; the OCSP responder
// URL comes from the certificate's Authority Information Access.

package registry

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/ocsp"
)

const (
	// crlCacheTTL bounds how long a fetched CRL is trusted; CRLs are
	// typically published with their own nextUpdate but a short TTL keeps
	// revocation latency bounded even when the list is stale.
	crlCacheTTL = 5 * time.Minute
	// crlNegativeTTL throttles retries against an unreachable CRL endpoint.
	crlNegativeTTL = 30 * time.Second
	// crlFetchTimeout bounds a single CRL HTTP round trip.
	crlFetchTimeout = 5 * time.Second
	// crlMaxEntries bounds the in-process CRL cache.
	crlMaxEntries = 64
)

// RevocationChecker decides whether a certificate is revoked. It is the
// seam that lets tests inject CRLs/OCSP answers without an HTTP server.
type RevocationChecker interface {
	// Revoked reports whether cert is revoked. issuer is the certificate
	// that signed cert (from the verified TLS chain) and is needed to build
	// and verify an OCSP request; it may be nil, in which case only CRL is
	// consulted. A non-nil error means the check could not be completed;
	// callers apply a fail-closed policy. When overrideURL is non-empty it
	// takes precedence over the certificate's CRL distribution points.
	Revoked(ctx context.Context, cert, issuer *x509.Certificate, overrideURL string) (bool, error)
}

// revocationChecker combines OCSP with a CRL fallback.
type revocationChecker struct {
	ocsp *ocspResolver
	crl  *crlResolver
}

// NewRevocationChecker builds an HTTP-backed OCSP + CRL checker for
// wiring.
func NewRevocationChecker() RevocationChecker {
	return &revocationChecker{
		ocsp: newOCSPResolver(),
		crl:  newCRLResolver(),
	}
}

// Revoked prefers a conclusive OCSP answer and falls back to CRL when the
// certificate advertises no OCSP responder or the responder cannot
// answer. CRL fetch/parse errors propagate so the caller fails closed.
func (c *revocationChecker) Revoked(ctx context.Context, cert, issuer *x509.Certificate, overrideURL string) (bool, error) {
	switch status, err := c.ocsp.status(ctx, cert, issuer); {
	case err == nil && status == ocsp.Revoked:
		return true, nil
	case err == nil && status == ocsp.Good:
		return false, nil
	}
	return c.crl.Revoked(ctx, cert, overrideURL)
}

// crlEntry is one cached revocation list.
type crlEntry struct {
	list        *x509.RevocationList
	fetchedAt   time.Time
	lastFailure time.Time
}

// crlResolver fetches and caches certificate revocation lists.
type crlResolver struct {
	mu    sync.Mutex
	cache map[string]*crlEntry
	fetch func(ctx context.Context, url string) ([]byte, error)
	now   func() time.Time
}

// newCRLResolver builds an HTTP-backed CRL checker.
func newCRLResolver() *crlResolver {
	client := &http.Client{Timeout: crlFetchTimeout}
	r := &crlResolver{
		cache: make(map[string]*crlEntry),
		now:   time.Now,
	}
	r.fetch = func(ctx context.Context, url string) ([]byte, error) {
		return fetchCRL(ctx, client, url)
	}
	return r
}

// fetchCRL retrieves a CRL over HTTP(S).
func fetchCRL(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crl %s returned status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// Revoked implements RevocationChecker.
func (r *crlResolver) Revoked(ctx context.Context, cert *x509.Certificate, overrideURL string) (bool, error) {
	url := overrideURL
	if url == "" && len(cert.CRLDistributionPoints) > 0 {
		url = cert.CRLDistributionPoints[0]
	}
	if url == "" {
		// No revocation source configured: nothing to check against.
		return false, nil
	}

	list, err := r.list(ctx, url)
	if err != nil {
		return false, err
	}
	for _, revoked := range list.RevokedCertificateEntries {
		if revoked.SerialNumber != nil && cert.SerialNumber != nil &&
			revoked.SerialNumber.Cmp(cert.SerialNumber) == 0 {
			return true, nil
		}
	}
	return false, nil
}

// list returns the parsed CRL for url, fetching at most once per TTL.
func (r *crlResolver) list(ctx context.Context, url string) (*x509.RevocationList, error) {
	r.mu.Lock()
	entry := r.cache[url]
	now := r.now()
	fresh := entry != nil && entry.list != nil && now.Sub(entry.fetchedAt) < crlCacheTTL
	failing := entry != nil && now.Sub(entry.lastFailure) < crlNegativeTTL
	r.mu.Unlock()

	if fresh {
		return entry.list, nil
	}
	if failing {
		// Negative window: report unavailable rather than hammering; a
		// stale list is still returned below so a transient outage does
		// not fail revoked certificates open.
		if entry != nil && entry.list != nil {
			return entry.list, nil
		}
		return nil, fmt.Errorf("crl %s unavailable (cached failure)", url)
	}

	raw, err := r.fetch(ctx, url)
	now = r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		cur := r.cache[url]
		if cur == nil {
			cur = &crlEntry{}
			r.cache[url] = cur
		}
		cur.lastFailure = now
		r.evictLocked()
		if cur.list != nil {
			return cur.list, nil
		}
		return nil, err
	}
	list, err := parseCRL(raw)
	if err != nil {
		cur := r.cache[url]
		if cur == nil {
			cur = &crlEntry{}
			r.cache[url] = cur
		}
		cur.lastFailure = now
		r.evictLocked()
		if cur.list != nil {
			return cur.list, nil
		}
		return nil, err
	}
	r.cache[url] = &crlEntry{list: list, fetchedAt: now}
	r.evictLocked()
	return list, nil
}

// evictLocked drops arbitrary entries past the bound.
func (r *crlResolver) evictLocked() {
	for len(r.cache) > crlMaxEntries {
		for k := range r.cache {
			delete(r.cache, k)
			break
		}
	}
}

// parseCRL accepts either DER or PEM encoded CRLs.
func parseCRL(raw []byte) (*x509.RevocationList, error) {
	if block, _ := pem.Decode(raw); block != nil {
		raw = block.Bytes
	}
	return x509.ParseRevocationList(raw)
}
