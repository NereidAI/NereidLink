// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command license-sign is the offline vendor tool that produces a signed
// NereidLink license envelope accepted by `nereidlink license install`.
//
// It signs the canonical JSON payload with an Ed25519 private key whose
// matching public key is the deployment trust anchor. The signing key is
// supplied by path and must be kept outside the repository and container
// images; only the verification public key is distributed.
//
// Example (deployment license, unlimited devices/tenants, 10 years):
//
//	go run ./tools/license-sign \
//	  --key configs/license/private/vendor-ed25519.pem \
//	  --scope '*' --customer 'Internal' --years 10 \
//	  --out /tmp/opencode/deployment.license.json
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/license"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "license-sign:", err)
		os.Exit(1)
	}
}

// run parses flags, loads the Ed25519 signing key, builds and validates the
// payload, signs its exact JSON bytes and writes the base64 envelope. It
// returns an error instead of writing an invalid or unsigned file.
func run() error {
	keyPath := flag.String("key", "", "path to the Ed25519 PKCS#8 private PEM key")
	outPath := flag.String("out", "", "output path for the signed license JSON envelope")
	scope := flag.String("scope", "*", "license scope: '*' for a deployment license, else a tenant UUID")
	customer := flag.String("customer", "", "contract/customer display name")
	deviceCap := flag.Int64("device-cap", 0, "device cap; 0 means unlimited")
	tenantCap := flag.Int64("tenant-cap", 0, "tenant cap (deployment scope only); 0 means unlimited")
	issued := flag.String("issued", "", "issue time RFC3339; defaults to now UTC")
	expires := flag.String("expires", "", "expiry time RFC3339; mutually exclusive with --years")
	years := flag.Int("years", 0, "convenience: validity length in years from issue time")
	featuresFlag := flag.String("features", "", "comma-separated purchased feature identifiers")
	flag.Parse()

	if *keyPath == "" || *outPath == "" {
		return fmt.Errorf("--key and --out are required")
	}
	if *expires != "" && *years != 0 {
		return fmt.Errorf("--expires and --years are mutually exclusive")
	}

	issuedAt := time.Now().UTC()
	if *issued != "" {
		parsed, err := time.Parse(time.RFC3339, *issued)
		if err != nil {
			return fmt.Errorf("parse --issued: %w", err)
		}
		issuedAt = parsed.UTC()
	}
	expiresAt, err := resolveExpiry(issuedAt, *expires, *years)
	if err != nil {
		return err
	}

	payload := license.Payload{
		Scope:        *scope,
		CustomerName: *customer,
		DeviceCap:    *deviceCap,
		TenantCap:    *tenantCap,
		IssuedAt:     issuedAt,
		ExpiresAt:    expiresAt,
		Features:     parseFeatures(*featuresFlag),
	}
	// Tenant licenses carry the tenant id verbatim; deployment licenses (*)
	// leave it empty. Validate enforces scope/tenant_id consistency.
	if *scope != "*" {
		payload.TenantID = *scope
	}
	if err := payload.Validate(); err != nil {
		return err
	}

	signingKey, err := loadPrivateKey(*keyPath)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	signature := ed25519.Sign(signingKey, body)

	envelope := license.File{
		Payload:   base64.StdEncoding.EncodeToString(body),
		Signature: base64.StdEncoding.EncodeToString(signature),
	}
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if err := os.WriteFile(*outPath, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	fmt.Printf("signed %s license scope=%s expires=%s -> %s\n",
		scopeKind(*scope), *scope, expiresAt.Format(time.RFC3339), *outPath)
	return nil
}

// loadPrivateKey reads and parses an Ed25519 private key in PKCS#8 PEM form.
// It rejects any other algorithm so the envelope is always signed with the
// key type the verifier expects.
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied signing key path
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("expected PKCS#8 PRIVATE KEY PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not Ed25519")
	}
	return key, nil
}

// resolveExpiry derives the expiry either from an explicit RFC3339 value or
// from the --years convenience offset, and requires the result to be in the
// future relative to issue time.
func resolveExpiry(issuedAt time.Time, expires string, years int) (time.Time, error) {
	switch {
	case expires != "":
		parsed, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse --expires: %w", err)
		}
		return parsed.UTC(), nil
	case years > 0:
		return issuedAt.AddDate(years, 0, 0), nil
	default:
		return time.Time{}, fmt.Errorf("one of --expires or --years is required")
	}
}

// parseFeatures turns the comma-separated flag into a non-nil slice; an empty
// flag yields an empty (but present) feature array required by Validate.
func parseFeatures(raw string) []string {
	features := []string{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			features = append(features, item)
		}
	}
	return features
}

// scopeKind labels a scope for operator output.
func scopeKind(scope string) string {
	if scope == "*" {
		return "deployment"
	}
	return "tenant"
}
