// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// jwks.go verifies device JWTs against remote JSON Web Key Sets
// (RFC 7517), satisfying the ADR-0009 requirement that JWT verification is
// local on the connection hot path: a key set is fetched over the network
// at most once per TTL and cached in-process, so steady-state connections
// perform no network I/O. The platform has no Redis, so the cache is a
// bounded in-process map. Concurrent cold fetches to one URI are merged
// with singleflight, and failed fetches are negatively cached to avoid
// hammering an unavailable issuer. JWKS verification is asymmetric-only
// (RS256/ES256): accepting HMAC here would enable the classic
// "public key as shared secret" algorithm-confusion attack.

package registry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/nereidai/nereidlink/internal/credverify"
)

const (
	// jwksCacheTTL is how long a successfully fetched key set is trusted
	// before a background refresh is allowed.
	jwksCacheTTL = 10 * time.Minute
	// jwksNegativeTTL throttles retries against an unreachable issuer.
	jwksNegativeTTL = 5 * time.Second
	// jwksFetchTimeout bounds a single key-set HTTP round trip.
	jwksFetchTimeout = 5 * time.Second
	// jwksMaxURIs bounds the in-process cache so credentials cannot make
	// it grow without limit.
	jwksMaxURIs = 256
)

// KeyResolver selects a public key for a JWKS-backed token. The interface
// is owned by internal/credverify so authentication at the registry RPC and
// from a credential cache resolve against one contract.
type KeyResolver = credverify.KeyResolver

// keyResolver is the package-internal alias used by the auth server.
type keyResolver = credverify.KeyResolver

// jwk is the RFC 7517 key subset needed for RSA/EC verification.
type jwk struct {
	// Kty is the key type ("RSA" or "EC").
	Kty string `json:"kty"`
	// Kid identifies the key within the set.
	Kid string `json:"kid"`
	// Alg, when present, pins the intended algorithm.
	Alg string `json:"alg"`
	// Use, when present, must be "sig" for verification keys.
	Use string `json:"use"`
	// N/E are the RSA modulus and exponent (base64url uints).
	N string `json:"n"`
	E string `json:"e"`
	// Crv/X/Y are the EC curve and point coordinates.
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwksDocument is the JWKS container.
type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

// jwksEntry is one cached key set.
type jwksEntry struct {
	// keys maps kid to a parsed public key.
	keys map[string]any
	// fetchedAt is when the set was last successfully retrieved.
	fetchedAt time.Time
	// lastFailure throttles re-fetch attempts when the URI is down.
	lastFailure time.Time
}

// jwksResolver caches remote key sets in-process.
type jwksResolver struct {
	mu     sync.Mutex
	cache  map[string]*jwksEntry
	flight singleflight.Group
	fetch  func(ctx context.Context, uri string) (jwksDocument, error)
	now    func() time.Time
}

// newJWKSResolver builds a resolver fetching sets over HTTP(S).
func newJWKSResolver() *jwksResolver {
	client := &http.Client{Timeout: jwksFetchTimeout}
	r := &jwksResolver{
		cache: make(map[string]*jwksEntry),
		now:   time.Now,
	}
	r.fetch = func(ctx context.Context, uri string) (jwksDocument, error) {
		return fetchJWKS(ctx, client, uri)
	}
	return r
}

// Compile-time assertion that the resolver satisfies the seam.
var _ keyResolver = (*jwksResolver)(nil)

// NewJWKSResolver builds an HTTP-backed resolver for wiring from main.
func NewJWKSResolver() KeyResolver {
	return newJWKSResolver()
}

// fetchJWKS retrieves and decodes a key set over HTTP.
func fetchJWKS(ctx context.Context, client *http.Client, uri string) (jwksDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return jwksDocument{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return jwksDocument{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return jwksDocument{}, fmt.Errorf("jwks %s returned status %d", uri, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jwksDocument{}, err
	}
	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return jwksDocument{}, err
	}
	return doc, nil
}

// ResolveKey implements keyResolver.
func (r *jwksResolver) ResolveKey(ctx context.Context, jwksURI, kid, alg string) (any, bool) {
	if kid == "" {
		// Without a kid the correct key cannot be selected unambiguously.
		return nil, false
	}

	r.mu.Lock()
	entry := r.cache[jwksURI]
	now := r.now()
	fresh := entry != nil && now.Sub(entry.fetchedAt) < jwksCacheTTL
	// While the negative window is open, avoid another network round trip;
	// a previously fetched (stale) set may still be used below.
	failing := entry != nil && now.Sub(entry.lastFailure) < jwksNegativeTTL
	r.mu.Unlock()

	if !fresh {
		if !failing {
			entry = r.refresh(ctx, jwksURI, entry)
		}
		if entry == nil || entry.keys == nil {
			return nil, false
		}
	}

	key, ok := entry.keys[kid]
	if !ok {
		// A kid absent from a current set must not trigger a hot-path
		// refetch; the next TTL boundary picks up rotation.
		return nil, false
	}
	if !keyAlgMatches(key, alg) {
		return nil, false
	}
	return key, true
}

// refresh fetches the set, merging concurrent callers for one URI, and
// updates the cache. stale is returned on failure when an older set exists
// so a transient issuer outage does not lock out every device.
func (r *jwksResolver) refresh(ctx context.Context, uri string, stale *jwksEntry) *jwksEntry {
	v, _, _ := r.flight.Do(uri, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
		defer cancel()
		doc, err := r.fetch(fetchCtx, uri)
		now := r.now()

		r.mu.Lock()
		defer r.mu.Unlock()
		if err != nil {
			cur := r.cache[uri]
			if cur == nil {
				cur = &jwksEntry{}
				r.cache[uri] = cur
			}
			cur.lastFailure = now
			r.evictOverflowLocked()
			return staleOrCached(stale, cur), nil
		}
		keys := parseJWKSet(doc)
		entry := &jwksEntry{keys: keys, fetchedAt: now}
		r.cache[uri] = entry
		r.evictOverflowLocked()
		return entry, nil
	})
	entry, _ := v.(*jwksEntry)
	return entry
}

// staleOrCached prefers the in-memory stale set passed by the caller, then
// whatever is already cached, and nil when nothing is usable.
func staleOrCached(stale, cached *jwksEntry) *jwksEntry {
	if stale != nil && stale.keys != nil {
		return stale
	}
	if cached != nil && cached.keys != nil {
		return cached
	}
	return nil
}

// evictOverflowLocked drops arbitrary entries past the bound.
func (r *jwksResolver) evictOverflowLocked() {
	for len(r.cache) > jwksMaxURIs {
		for k := range r.cache {
			delete(r.cache, k)
			break
		}
	}
}

// parseJWKSet converts usable signing keys, skipping malformed or
// unsupported entries rather than failing the whole set.
func parseJWKSet(doc jwksDocument) map[string]any {
	keys := make(map[string]any, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kid == "" {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			if key, err := parseRSAJWK(k); err == nil {
				keys[k.Kid] = key
			}
		case "EC":
			if key, err := parseECJWK(k); err == nil {
				keys[k.Kid] = key
			}
		}
	}
	return keys
}

// decodeJWKUint decodes a base64url big-endian unsigned integer.
func decodeJWKUint(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		// Some emitters pad; tolerate that.
		raw, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	return new(big.Int).SetBytes(raw), nil
}

// parseRSAJWK builds an RSA public key from its JWK fields.
func parseRSAJWK(k jwk) (*rsa.PublicKey, error) {
	n, err := decodeJWKUint(k.N)
	if err != nil {
		return nil, err
	}
	e, err := decodeJWKUint(k.E)
	if err != nil {
		return nil, err
	}
	if !e.IsInt64() {
		return nil, fmt.Errorf("rsa exponent too large")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// parseECJWK builds a P-256 public key from its JWK fields; ES256 is the
// only EC algorithm accepted so other curves are rejected here. It builds
// the SEC1 uncompressed point (0x04 || X || Y, each 32 bytes) and parses
// it with the modern constructor rather than assembling a struct from
// deprecated coordinate fields.
func parseECJWK(k jwk) (*ecdsa.PublicKey, error) {
	if k.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	x, err := decodeJWKUint(k.X)
	if err != nil {
		return nil, err
	}
	y, err := decodeJWKUint(k.Y)
	if err != nil {
		return nil, err
	}
	point := make([]byte, 1+2*32)
	point[0] = 0x04
	x.FillBytes(point[1:33])
	y.FillBytes(point[33:65])
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
}

// keyAlgMatches confirms a parsed key's type matches the token algorithm,
// the second half of the algorithm-confusion defense (kid selection is the
// first).
func keyAlgMatches(key any, alg string) bool {
	switch alg {
	case "RS256":
		_, ok := key.(*rsa.PublicKey)
		return ok
	case "ES256":
		k, ok := key.(*ecdsa.PublicKey)
		return ok && k.Params().BitSize == 256
	default:
		return false
	}
}

// validateJWKSURI validates a configured key-set URL. HTTPS is required
// except for loopback HTTP, which keeps local development and tests usable.
func validateJWKSURI(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid jwks_uri %q", raw)
	}
	host := parsed.Hostname()
	loopback := host == "127.0.0.1" || host == "::1" || host == "localhost"
	if parsed.Scheme == "https" || (parsed.Scheme == "http" && loopback) {
		return nil
	}
	return fmt.Errorf("jwks_uri must use https (loopback http allowed): %q", raw)
}
