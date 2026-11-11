// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// jwks_test.go covers remote JWK Set verification of device JWTs:
// RSA/EC happy paths, kid and algorithm-confusion rejection, TTL caching,
// singleflight coalescing, negative caching and fail-closed behavior when
// the issuer is unreachable.

package registry

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/credverify"
)

// rsaJWK builds the JWK form of an RSA public key.
func rsaJWK(t *testing.T, kid string, key *rsa.PublicKey) jwk {
	t.Helper()
	return jwk{
		Kty: "RSA",
		Kid: kid,
		Alg: "RS256",
		Use: "sig",
		N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(bigEndian(key.E)),
	}
}

// ecJWK builds the JWK form of a P-256 public key.
func ecJWK(t *testing.T, kid string, key *ecdsa.PublicKey) jwk {
	t.Helper()
	point, err := key.Bytes() // SEC1 uncompressed: 0x04 || X || Y.
	if err != nil {
		t.Fatalf("encode ec point: %v", err)
	}
	return jwk{
		Kty: "EC",
		Kid: kid,
		Alg: "ES256",
		Use: "sig",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(point[1:33]),
		Y:   base64.RawURLEncoding.EncodeToString(point[33:65]),
	}
}

// newTestResolver wires a resolver with a controllable clock and fetch
// function; the returned clock advances resolver time.
func newTestResolver(fetch func(context.Context, string) (jwksDocument, error)) (*jwksResolver, func(time.Duration)) {
	now := time.Unix(1_700_000_000, 0)
	r := &jwksResolver{
		cache: make(map[string]*jwksEntry),
		now:   func() time.Time { return now },
		fetch: fetch,
	}
	return r, func(d time.Duration) { now = now.Add(d) }
}

// signRS256 produces a compact RS256 JWT with the given header/payload.
func signRS256(t *testing.T, key *rsa.PrivateKey, header map[string]any, payload map[string]any) string {
	t.Helper()
	input := jwtPart(t, header) + "." + jwtPart(t, payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign rs256: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// signES256 produces a compact ES256 JWT with fixed-width R||S signature.
func signES256(t *testing.T, key *ecdsa.PrivateKey, header map[string]any, payload map[string]any) string {
	t.Helper()
	input := jwtPart(t, header) + "." + jwtPart(t, payload)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatalf("sign es256: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksExtra is the credential extra JSONB pinning a remote key set.
func jwksExtra(t *testing.T, uri string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"jwks_uri": uri})
	if err != nil {
		t.Fatalf("marshal extra: %v", err)
	}
	return raw
}

// TestJWKSRSA verifies an RS256 token against a JWKS key selected by kid.
func TestJWKSRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	doc := jwksDocument{Keys: []jwk{rsaJWK(t, "key-1", &key.PublicKey)}}
	r, _ := newTestResolver(func(context.Context, string) (jwksDocument, error) { return doc, nil })

	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "key-1"}
	token := signRS256(t, key, header, futureClaims())

	ok, err := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: jwksExtra(t, uri)}, token, "", r)
	if err != nil || !ok {
		t.Fatalf("jwks rs256 valid token must pass, ok=%v err=%v", ok, err)
	}
}

// TestJWKSEC verifies an ES256 token against a P-256 JWKS key.
func TestJWKSEC(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	doc := jwksDocument{Keys: []jwk{ecJWK(t, "ec-1", &key.PublicKey)}}
	r, _ := newTestResolver(func(context.Context, string) (jwksDocument, error) { return doc, nil })

	header := map[string]any{"alg": "ES256", "typ": "JWT", "kid": "ec-1"}
	token := signES256(t, key, header, futureClaims())

	ok, err := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: jwksExtra(t, uri)}, token, "", r)
	if err != nil || !ok {
		t.Fatalf("jwks es256 valid token must pass, ok=%v err=%v", ok, err)
	}
}

// TestJWKSRejections covers unknown kid, missing kid and HMAC confusion.
func TestJWKSRejections(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	doc := jwksDocument{Keys: []jwk{rsaJWK(t, "key-1", &rsaKey.PublicKey)}}
	r, _ := newTestResolver(func(context.Context, string) (jwksDocument, error) { return doc, nil })
	extra := jwksExtra(t, uri)

	// Unknown kid.
	tok := signRS256(t, rsaKey, map[string]any{"alg": "RS256", "kid": "other"}, futureClaims())
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, tok, "", r); ok {
		t.Fatal("unknown kid must fail")
	}
	// Missing kid cannot select a key unambiguously.
	tok = signRS256(t, rsaKey, map[string]any{"alg": "RS256"}, futureClaims())
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, tok, "", r); ok {
		t.Fatal("missing kid must fail")
	}
	// HMAC algorithm must never be verified against a JWKS (confusion).
	hmacTok := hmacToken(t, "shared", map[string]any{"alg": "HS256", "kid": "key-1"}, futureClaims())
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, hmacTok, "", r); ok {
		t.Fatal("HS256 token must not verify against JWKS")
	}
	// A nil resolver fails closed.
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, tok, "", nil); ok {
		t.Fatal("nil resolver must fail closed")
	}
}

// TestJWKSCacheTTL ensures a fresh set is served without refetching and an
// expired set triggers one refresh.
func TestJWKSCacheTTL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	doc := jwksDocument{Keys: []jwk{rsaJWK(t, "key-1", &key.PublicKey)}}
	var calls int32
	r, advance := newTestResolver(func(context.Context, string) (jwksDocument, error) {
		atomic.AddInt32(&calls, 1)
		return doc, nil
	})
	header := map[string]any{"alg": "RS256", "kid": "key-1"}
	token := signRS256(t, key, header, futureClaims())

	for i := 0; i < 3; i++ {
		if ok, _ := credverify.Verify(context.Background(),
			credverify.Record{Type: "JWT", Extra: jwksExtra(t, uri)}, token, "", r); !ok {
			t.Fatal("cached set must keep verifying")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", got)
	}
	advance(jwksCacheTTL + time.Second)
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: jwksExtra(t, uri)}, token, "", r); !ok {
		t.Fatal("verification after TTL refresh must pass")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected refetch after TTL, got %d calls", got)
	}
}

// TestJWKSNegativeCache ensures failures fail closed, are retried only
// after the negative window, and a stale set survives a transient outage.
func TestJWKSNegativeCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	good := jwksDocument{Keys: []jwk{rsaJWK(t, "key-1", &key.PublicKey)}}
	var down int32
	var calls int32
	r, advance := newTestResolver(func(context.Context, string) (jwksDocument, error) {
		atomic.AddInt32(&calls, 1)
		if atomic.LoadInt32(&down) == 1 {
			return jwksDocument{}, fmt.Errorf("issuer unavailable")
		}
		return good, nil
	})
	extra := jwksExtra(t, uri)
	header := map[string]any{"alg": "RS256", "kid": "key-1"}
	token := signRS256(t, key, header, futureClaims())

	// Initial success.
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, token, "", r); !ok {
		t.Fatal("initial fetch must succeed")
	}
	// Issuer goes down after the set expires: refresh fails, but the stale
	// set keeps authenticating within its tolerated outage window.
	advance(jwksCacheTTL + time.Second)
	atomic.StoreInt32(&down, 1)
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, token, "", r); !ok {
		t.Fatal("stale set should tolerate a transient fetch failure")
	}
	failedCalls := atomic.LoadInt32(&calls)
	// Within the negative window no further fetch is attempted.
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: extra}, token, "", r); !ok {
		t.Fatal("stale set should still verify during negative window")
	}
	if got := atomic.LoadInt32(&calls); got != failedCalls {
		t.Fatalf("negative cache must suppress refetch, got %d vs %d", got, failedCalls)
	}

	// A never-seen URI fails closed while down and is negatively cached.
	freshURI := "https://other.example/jwks.json"
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: jwksExtra(t, freshURI)}, token, "", r); ok {
		t.Fatal("unreachable new issuer must fail closed")
	}
	before := atomic.LoadInt32(&calls)
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Extra: jwksExtra(t, freshURI)}, token, "", r); ok {
		t.Fatal("unreachable new issuer must stay failed")
	}
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatal("failure must be negatively cached")
	}
}

// TestJWKSSingleflight coalesces concurrent cold fetches to one URI.
func TestJWKSSingleflight(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := "https://issuer.example/jwks.json"
	doc := jwksDocument{Keys: []jwk{rsaJWK(t, "key-1", &key.PublicKey)}}
	var calls int32
	release := make(chan struct{})
	var once sync.Once
	r, _ := newTestResolver(func(context.Context, string) (jwksDocument, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(release) })
		time.Sleep(20 * time.Millisecond)
		return doc, nil
	})
	header := map[string]any{"alg": "RS256", "kid": "key-1"}
	token := signRS256(t, key, header, futureClaims())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := credverify.Verify(context.Background(),
				credverify.Record{Type: "JWT", Extra: jwksExtra(t, uri)}, token, "", r); !ok {
				t.Error("concurrent verification must pass")
			}
		}()
	}
	wg.Wait()
	select {
	case <-release:
	default:
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 coalesced fetch, got %d", got)
	}
}

// TestValidateJWKSURI enforces https except loopback http.
func TestValidateJWKSURI(t *testing.T) {
	good := []string{
		"https://issuer.example/jwks.json",
		"http://127.0.0.1:8080/jwks",
		"http://localhost/jwks",
		"http://[::1]/jwks",
	}
	for _, uri := range good {
		if err := validateJWKSURI(uri); err != nil {
			t.Fatalf("uri %q should be allowed: %v", uri, err)
		}
	}
	bad := []string{
		"http://issuer.example/jwks.json",
		"not a url",
		"ftp://issuer.example/jwks.json",
		"",
	}
	for _, uri := range bad {
		if err := validateJWKSURI(uri); err == nil {
			t.Fatalf("uri %q should be rejected", uri)
		}
	}
}

// bigEndian returns the minimal big-endian bytes of an RSA exponent.
func bigEndian(e int) []byte {
	if e == 0 {
		return []byte{0}
	}
	var out []byte
	for v := e; v > 0; v >>= 8 {
		out = append([]byte{byte(v & 0xff)}, out...)
	}
	return out
}

// Ensure the parsed RSA key round-trips through x509, catching exponent
// encoding mistakes that would only otherwise show at verify time.
func TestParseRSAJWKRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	parsed, err := parseRSAJWK(rsaJWK(t, "k", &key.PublicKey))
	if err != nil {
		t.Fatalf("parse rsa jwk: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(parsed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		t.Fatalf("round trip: %v", err)
	}
}
