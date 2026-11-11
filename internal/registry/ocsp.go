// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ocsp.go checks an X.509 device certificate against its issuer's OCSP
// responder, per ADR-0009 ("certificate-chain validation + CRL/OCSP checks"). The responder
// URL is taken from the certificate's Authority Information Access. OCSP
// complements CRL with fresher revocation data, so the connection hot path
// must not depend on the responder: conclusive Good/Revoked answers are
// cached in-process for a short TTL (no Redis), and an unreachable or
// unparseable answer is reported as an error so the caller can fall back
// to CRL and ultimately apply its fail-closed policy.

package registry

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/ocsp"
)

const (
	// ocspCacheTTL bounds how long a conclusive OCSP answer is trusted.
	ocspCacheTTL = 5 * time.Minute
	// ocspNegativeTTL throttles retries against an unreachable responder.
	ocspNegativeTTL = 30 * time.Second
	// ocspFetchTimeout bounds a single OCSP HTTP round trip.
	ocspFetchTimeout = 5 * time.Second
	// ocspMaxEntries bounds the in-process OCSP cache.
	ocspMaxEntries = 64
)

// errOCSPThrottled signals that a recent responder failure is still inside
// its negative-cache window, so no network call is attempted.
var errOCSPThrottled = errors.New("ocsp responder unavailable (cached failure)")

// ocspEntry is one cached OCSP answer for one certificate.
type ocspEntry struct {
	status      int
	expiresAt   time.Time
	lastFailure time.Time
}

// ocspResolver builds OCSP requests, posts them to the responder and
// caches the parsed answer.
type ocspResolver struct {
	mu    sync.Mutex
	cache map[string]*ocspEntry
	fetch func(ctx context.Context, url string, body []byte) ([]byte, error)
	now   func() time.Time
}

// newOCSPResolver builds an HTTP-backed OCSP resolver.
func newOCSPResolver() *ocspResolver {
	client := &http.Client{Timeout: ocspFetchTimeout}
	r := &ocspResolver{
		cache: make(map[string]*ocspEntry),
		now:   time.Now,
	}
	r.fetch = func(ctx context.Context, url string, body []byte) ([]byte, error) {
		return fetchOCSP(ctx, client, url, body)
	}
	return r
}

// fetchOCSP posts a DER-encoded OCSP request and returns the raw response.
func fetchOCSP(ctx context.Context, client *http.Client, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ocsp-request")
	req.Header.Set("Accept", "application/ocsp-response")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ocsp %s returned status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// ocspCacheState classifies a cached entry: miss (fetch now), positive
// (a conclusive answer still fresh) or negative (a recent failure within
// the throttle window).
type ocspCacheState int

const (
	ocspMiss ocspCacheState = iota
	ocspPositive
	ocspNegative
)

// status queries the responder for cert, returning an ocsp status
// (Good/Revoked/Unknown). A certificate with no OCSP responder yields
// Unknown with no error. A throttled failure returns errOCSPThrottled
// without a network call; any other failure is returned as a normal
// error. The caller treats both as "no OCSP answer" and falls back to
// CRL.
func (o *ocspResolver) status(ctx context.Context, cert, issuer *x509.Certificate) (int, error) {
	if issuer == nil || len(cert.OCSPServer) == 0 {
		return ocsp.Unknown, nil
	}
	url := cert.OCSPServer[0]
	key := ocspCacheKey(url, cert)

	switch state, status := o.cacheState(key); state {
	case ocspPositive:
		return status, nil
	case ocspNegative:
		return ocsp.Unknown, errOCSPThrottled
	}

	request, err := ocsp.CreateRequest(cert, issuer, &ocsp.RequestOptions{})
	if err != nil {
		return ocsp.Unknown, fmt.Errorf("build ocsp request: %w", err)
	}
	raw, err := o.fetch(ctx, url, request)
	if err != nil {
		o.markFailure(key)
		return ocsp.Unknown, err
	}
	response, err := ocsp.ParseResponseForCert(raw, cert, issuer)
	if err != nil {
		o.markFailure(key)
		return ocsp.Unknown, fmt.Errorf("parse ocsp response: %w", err)
	}

	expiresAt := o.now().Add(ocspCacheTTL)
	if !response.NextUpdate.IsZero() && response.NextUpdate.Before(expiresAt) {
		expiresAt = response.NextUpdate
	}
	o.store(key, response.Status, expiresAt)
	return response.Status, nil
}

// cacheState classifies the entry for key.
func (o *ocspResolver) cacheState(key string) (ocspCacheState, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	entry := o.cache[key]
	if entry == nil {
		return ocspMiss, ocsp.Unknown
	}
	if !entry.expiresAt.IsZero() && now.Before(entry.expiresAt) {
		return ocspPositive, entry.status
	}
	if now.Sub(entry.lastFailure) < ocspNegativeTTL {
		return ocspNegative, ocsp.Unknown
	}
	return ocspMiss, ocsp.Unknown
}

// markFailure records a failed fetch for negative caching.
func (o *ocspResolver) markFailure(key string) {
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := o.cache[key]
	if entry == nil {
		entry = &ocspEntry{}
		o.cache[key] = entry
	}
	entry.lastFailure = now
	o.evictLocked()
}

// store records a conclusive answer with its expiry.
func (o *ocspResolver) store(key string, status int, expiresAt time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cache[key] = &ocspEntry{status: status, expiresAt: expiresAt}
	o.evictLocked()
}

// evictLocked drops arbitrary entries past the bound.
func (o *ocspResolver) evictLocked() {
	for len(o.cache) > ocspMaxEntries {
		for k := range o.cache {
			delete(o.cache, k)
			break
		}
	}
}

// ocspCacheKey identifies one certificate's answer at one responder.
func ocspCacheKey(url string, cert *x509.Certificate) string {
	return fmt.Sprintf("%s|%x|%x", url, cert.AuthorityKeyId, cert.SerialNumber.Bytes())
}
