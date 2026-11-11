// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// auth_credential_test.go covers mechanism dispatch and the device JWT
// verifier: constant-time TOKEN/PASSWORD comparison, argon2 PHC
// verification, HS256/RS256/ES256 signatures, claim constraints, X509
// password-path rejection and certificate fingerprint verification.

package registry

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/credverify"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/pki"
	"github.com/nereidai/nereidlink/internal/store"
)

// jwtPart base64raw-encodes a JSON value for compact JWT assembly.
func jwtPart(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal jwt part: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// hmacToken builds a valid HS256 token for signingInputParts.
func hmacToken(t *testing.T, secret string, header, payload map[string]any) string {
	t.Helper()
	signingInput := jwtPart(t, header) + "." + jwtPart(t, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// pemPublic wraps a PKIX public key DER in a PEM block.
func pemPublic(t *testing.T, der []byte) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// futureClaims returns standard claims expiring in an hour.
func futureClaims() map[string]any {
	return map[string]any{
		"sub": "device-001",
		"iss": "issuer-a",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

// TestVerifyToken covers TOKEN constant-time comparison.
func TestVerifyToken(t *testing.T) {
	stored := store.StoredCredential{Type: "TOKEN", CredentialsValue: "abc123"}
	cases := []struct {
		name      string
		presented string
		want      bool
	}{
		{"match", "abc123", true},
		{"wrong", "abc124", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := verifyCredential(context.Background(), stored, tc.presented, "", nil)
			if err != nil || ok != tc.want {
				t.Fatalf("got ok=%v err=%v, want %v", ok, err, tc.want)
			}
		})
	}
}

// TestVerifyPassword covers argon2 PHC and legacy plaintext fallback.
func TestVerifyPassword(t *testing.T) {
	phc, err := identity.HashSecret("S3cretPass!")
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	cases := []struct {
		name      string
		stored    string
		presented string
		want      bool
	}{
		{"phc match", phc, "S3cretPass!", true},
		{"phc wrong", phc, "other", false},
		{"legacy match", "legacy-pw", "legacy-pw", true},
		{"legacy wrong", "legacy-pw", "nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := store.StoredCredential{Type: "PASSWORD", CredentialsValue: tc.stored}
			ok, err := verifyCredential(context.Background(), stored, tc.presented, "", nil)
			if err != nil || ok != tc.want {
				t.Fatalf("got ok=%v err=%v, want %v", ok, err, tc.want)
			}
		})
	}
}

// TestVerifyJWT_HS256 covers shared-secret verification and constraints.
func TestVerifyJWT_HS256(t *testing.T) {
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	payload := futureClaims()
	token := hmacToken(t, "hmac-secret", header, payload)

	ok, err := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: "hmac-secret"}, token, "", nil)
	if err != nil || !ok {
		t.Fatalf("hs256 valid token: ok=%v err=%v", ok, err)
	}

	ok, err = credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: "wrong-secret"}, token, "", nil)
	if err != nil || ok {
		t.Fatalf("hs256 wrong key must fail, got ok=%v err=%v", ok, err)
	}

	// The audience constraint pins aud, which the token does not carry.
	extra := []byte(`{"iss":"issuer-a","sub":"device-001","aud":"tenant-x"}`)
	ok, err = credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: "hmac-secret", Extra: extra}, token, "", nil)
	if err != nil || ok {
		t.Fatalf("missing aud with constraint must fail, got ok=%v err=%v", ok, err)
	}
}

// TestVerifyJWT_RS256 covers RSA public-key verification.
func TestVerifyJWT_RS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	payload := futureClaims()
	signingInput := jwtPart(t, header) + "." + jwtPart(t, payload)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign rs256: %v", err)
	}
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	ok, err := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: pemPublic(t, der)}, token, "", nil)
	if err != nil || !ok {
		t.Fatalf("rs256 valid token: ok=%v err=%v", ok, err)
	}
}

// TestVerifyJWT_ES256 covers fixed-width R||S ECDSA verification.
func TestVerifyJWT_ES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ec key: %v", err)
	}
	header := map[string]any{"alg": "ES256", "typ": "JWT"}
	payload := futureClaims()
	signingInput := jwtPart(t, header) + "." + jwtPart(t, payload)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatalf("sign es256: %v", err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	ok, err := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: pemPublic(t, der)}, token, "", nil)
	if err != nil || !ok {
		t.Fatalf("es256 valid token: ok=%v err=%v", ok, err)
	}
}

// TestVerifyJWTExpiry covers expired token rejection.
func TestVerifyJWTExpiry(t *testing.T) {
	header := map[string]any{"alg": "HS256"}
	payload := map[string]any{"sub": "d", "exp": time.Now().Add(-time.Minute).Unix()}
	token := hmacToken(t, "k", header, payload)
	if ok, _ := credverify.Verify(context.Background(),
		credverify.Record{Type: "JWT", Secret: "k"}, token, "", nil); ok {
		t.Fatal("expired token must fail")
	}
}

// TestVerifyX509OverPassword ensures cert credentials cannot authenticate
// without the mTLS layer presenting a fingerprint.
func TestVerifyX509OverPassword(t *testing.T) {
	stored := store.StoredCredential{Type: "X509", CredentialsValue: "certdata"}
	if ok, _ := verifyCredential(context.Background(), stored, "anything", "", nil); ok {
		t.Fatal("x509 must not verify over username/password")
	}
}

// TestVerifyX509Certificate covers fingerprint matching using a certificate
// issued by the platform private PKI.
func TestVerifyX509Certificate(t *testing.T) {
	dir := t.TempDir()
	if err := pki.InitCA(dir, []string{"localhost"}); err != nil {
		t.Fatalf("init pki: %v", err)
	}
	keyPath := filepath.Join(dir, "device-key.pem")
	certPath := filepath.Join(dir, "device.pem")
	if err := pki.IssueDevice(dir, "x509-cred-1", keyPath, certPath, 0); err != nil {
		t.Fatalf("issue device cert: %v", err)
	}
	//nolint:gosec // path is inside the per-test temp directory
	pemCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read device cert: %v", err)
	}
	block, _ := pem.Decode(pemCert)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse device cert: %v", err)
	}
	fingerprint := certFingerprintSHA256(cert.Raw)
	stored := store.StoredCredential{Type: "X509", CredentialsValue: string(pemCert)}

	if ok, err := verifyCredential(context.Background(), stored, "", fingerprint, nil); err != nil || !ok {
		t.Fatalf("matching fingerprint must succeed, ok=%v err=%v", ok, err)
	}
	if ok, _ := verifyCredential(context.Background(), stored, "", strings.Repeat("0", 64), nil); ok {
		t.Fatal("wrong fingerprint must fail")
	}
	if ok, _ := verifyCredential(context.Background(), stored, "", "", nil); ok {
		t.Fatal("missing fingerprint must fail")
	}

	// Malformed stored certificate material resolves to an error and
	// therefore never authenticates.
	stored.CredentialsValue = "not a certificate"
	if ok, err := verifyCredential(context.Background(), stored, "", fingerprint, nil); err == nil || ok {
		t.Fatalf("malformed stored cert must fail, ok=%v err=%v", ok, err)
	}
}
