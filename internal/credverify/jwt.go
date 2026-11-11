// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// jwt.go verifies compact-serialization JWT credentials presented by
// devices, following the codebase convention of hand-rolling JWT
// operations with the standard library instead of pulling a third-party JWT
// dependency. Three algorithms are accepted: HS256 (shared secret), RS256
// (RSA public key PEM) and ES256 (P-256 public key PEM). The per-credential
// extra JSONB may pin issuer, subject and audience.

package credverify

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"
)

// jwtHeader is the protected header subset needed to select the
// verification algorithm and JWKS key.
type jwtHeader struct {
	// Alg is the JWA algorithm name.
	Alg string `json:"alg"`
	// Kid selects a key from a JWK Set when present.
	Kid string `json:"kid"`
}

// jwtConstraints pins token claims through the credential extra JSONB.
type jwtConstraints struct {
	// Issuer requires an exact iss match when non-empty.
	Issuer string `json:"iss"`
	// Subject requires an exact sub match when non-empty.
	Subject string `json:"sub"`
	// Audience requires aud to contain the value when non-empty.
	Audience string `json:"aud"`
	// JWKSURI, when set, verifies the token asymmetrically against a key
	// selected by kid from this remote JWK Set instead of using an inline
	// verification key.
	JWKSURI string `json:"jwks_uri"`
}

// errInvalidJWT marks malformed key material for device JWT verification.
var errInvalidJWT = errors.New("invalid device JWT key material")

// verifyJWT validates signature, time-based claims and the optional
// extra-JSON constraints of a device JWT. Any malformed input resolves to
// false rather than an error so authentication behaves like a wrong secret.
// resolver is used only when the credential pins a jwks_uri and may be nil
// for inline-key credentials.
func verifyJWT(ctx context.Context, token, verificationKey string, extra []byte, resolver KeyResolver) (bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false, nil
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false, nil
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return false, nil
	}

	var constraints jwtConstraints
	if len(extra) > 0 {
		_ = json.Unmarshal(extra, &constraints)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false, nil
	}
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))

	if constraints.JWKSURI != "" {
		// Remote key sets only authorize asymmetric algorithms; resolve
		// the kid-selected key and verify below. A nil resolver or any
		// resolution/algorithm mismatch fails closed.
		if resolver == nil {
			return false, nil
		}
		key, ok := resolver.ResolveKey(ctx, constraints.JWKSURI, header.Kid, header.Alg)
		if !ok || !verifyAsymmetric(header.Alg, key, sum[:], signature) {
			return false, nil
		}
	} else {
		if !verifyInline(header.Alg, verificationKey, signingInput, sum[:], signature) {
			return false, nil
		}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, nil
	}
	var claims struct {
		// Exp is mandatory for device credentials.
		Exp *int64 `json:"exp"`
		// Nbf is honored when present.
		Nbf *int64 `json:"nbf"`
		// Iss is checked against the extra-JSON constraint.
		Iss string `json:"iss"`
		// Sub is checked against the extra-JSON constraint.
		Sub string `json:"sub"`
		// Aud may be a single string or an array of strings.
		Aud any `json:"aud"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return false, nil
	}

	now := time.Now().Unix()
	if claims.Exp == nil || now >= *claims.Exp {
		return false, nil
	}
	if claims.Nbf != nil && now < *claims.Nbf {
		return false, nil
	}

	if constraints.Issuer != "" && constraints.Issuer != claims.Iss {
		return false, nil
	}
	if constraints.Subject != "" && constraints.Subject != claims.Sub {
		return false, nil
	}
	if constraints.Audience != "" && !audienceContains(claims.Aud, constraints.Audience) {
		return false, nil
	}
	return true, nil
}

// verifyInline checks a token signature against a credential's inline key
// material: HS256 shared secret, RS256 RSA PEM or ES256 P-256 PEM.
func verifyInline(alg, verificationKey, signingInput string, sum, signature []byte) bool {
	switch alg {
	case "HS256":
		mac := hmac.New(sha256.New, []byte(verificationKey))
		mac.Write([]byte(signingInput))
		return hmac.Equal(signature, mac.Sum(nil))
	case "RS256":
		key, err := parseRSAPublicKey(verificationKey)
		if err != nil {
			return false
		}
		return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum, signature) == nil
	case "ES256":
		key, err := parseECPublicKey(verificationKey)
		if err != nil || key.Params().BitSize != 256 || len(signature) != 64 {
			return false
		}
		// JWT ES256 carries fixed-width R||S rather than DER, so split
		// before invoking the ECDSA verifier.
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		return ecdsa.Verify(key, sum, r, s)
	default:
		return false
	}
}

// verifyAsymmetric checks a token signature against a kid-selected public
// key resolved from a JWK Set. Only RS256/ES256 are accepted so an HMAC or
// "none" token can never be verified with remote material.
func verifyAsymmetric(alg string, key any, sum, signature []byte) bool {
	switch alg {
	case "RS256":
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return false
		}
		return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, sum, signature) == nil
	case "ES256":
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok || ecKey.Params().BitSize != 256 || len(signature) != 64 {
			return false
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		return ecdsa.Verify(ecKey, sum, r, s)
	default:
		return false
	}
}

// audienceContains reports whether aud carries want; aud may be a string,
// []string or []any depending on how the JSON was produced.
func audienceContains(aud any, want string) bool {
	switch value := aud.(type) {
	case string:
		return value == want
	case []any:
		for _, entry := range value {
			if text, ok := entry.(string); ok && text == want {
				return true
			}
		}
	case []string:
		for _, entry := range value {
			if entry == want {
				return true
			}
		}
	}
	return false
}

// parseRSAPublicKey decodes an RSA public key from either PKCS#1 or PKIX
// PEM.
func parseRSAPublicKey(pemData string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errInvalidJWT
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errInvalidJWT
	}
	return key, nil
}

// parseECPublicKey decodes a P-256 public key from PKIX PEM.
func parseECPublicKey(pemData string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errInvalidJWT
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errInvalidJWT
	}
	return key, nil
}
