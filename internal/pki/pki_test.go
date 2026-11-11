// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// pki_test.go locks the private PKI lifecycle: CA/server creation,
// refusal to silently rotate an existing CA, device certificate issuance,
// and mTLS server configuration with an untrusted-client rejection.

package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// TestInitCAAndIssueDevice exercises the full local PKI workflow.
func TestInitCAAndIssueDevice(t *testing.T) {
	dir := t.TempDir()

	if err := InitCA(dir, nil); err == nil {
		t.Fatal("init without server hosts must fail")
	}
	if err := InitCA(dir, []string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatalf("init pki: %v", err)
	}
	for _, name := range []string{CACertFile, CAKeyFile, ServerCertFile, ServerKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}

	// Re-running init must not rotate an existing CA silently.
	if err := InitCA(dir, []string{"localhost"}); err == nil {
		t.Fatal("init over an existing CA must fail")
	}

	keyPath := filepath.Join(dir, "devices", "d1-key.pem")
	certPath := filepath.Join(dir, "devices", "d1.pem")
	if err := IssueDevice(dir, "cred-1", keyPath, certPath, 0); err != nil {
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
	if cert.Subject.CommonName != "cred-1" {
		t.Fatalf("unexpected cn %q", cert.Subject.CommonName)
	}
	if err := cert.CheckSignatureFrom(mustParsePEM(t, filepath.Join(dir, CACertFile))); err != nil {
		t.Fatalf("device cert not signed by CA: %v", err)
	}

	config, err := ServerTLSConfig(
		filepath.Join(dir, ServerCertFile),
		filepath.Join(dir, ServerKeyFile),
		filepath.Join(dir, CACertFile), false)
	if err != nil {
		t.Fatalf("server tls config: %v", err)
	}
	if config.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("unexpected client auth policy %v", config.ClientAuth)
	}

	strict, err := ServerTLSConfig(
		filepath.Join(dir, ServerCertFile),
		filepath.Join(dir, ServerKeyFile),
		filepath.Join(dir, CACertFile), true)
	if err != nil {
		t.Fatalf("strict tls config: %v", err)
	}
	if strict.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("unexpected strict policy %v", strict.ClientAuth)
	}
}

// TestIssueDeviceValidation rejects empty common names.
func TestIssueDeviceValidation(t *testing.T) {
	if err := IssueDevice(t.TempDir(), "", "k.pem", "c.pem", 0); err == nil {
		t.Fatal("empty cn must fail")
	}
}

// TestIssueServerRehostsWithoutRotatingCA covers the operational fix for
// ERR_TLS_CERT_ALTNAME_INVALID: an operator who dials a LAN address must
// be able to re-sign the server certificate for it without invalidating
// the CA, because rotating the CA would break every registered X509
// credential.
func TestIssueServerRehostsWithoutRotatingCA(t *testing.T) {
	dir := t.TempDir()
	if err := InitCA(dir, []string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatalf("InitCA: %v", err)
	}
	caBefore, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}

	// A device credential issued before the re-sign must stay valid.
	deviceKey := filepath.Join(dir, "devices", "dev-key.pem")
	deviceCert := filepath.Join(dir, "devices", "dev.pem")
	if err := IssueDevice(dir, "dev-1", deviceKey, deviceCert, 0); err != nil {
		t.Fatalf("IssueDevice: %v", err)
	}
	deviceBefore := mustParsePEM(t, deviceCert)

	if err := IssueServer(dir, []string{"localhost", "127.0.0.1", "192.0.2.57"}); err != nil {
		t.Fatalf("IssueServer: %v", err)
	}

	caAfter, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		t.Fatalf("read CA after: %v", err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("IssueServer must not change the CA certificate")
	}

	// The new name must verify, and the old one must still verify too.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caAfter)
	if err := mustParsePEM(t, filepath.Join(dir, ServerCertFile)).VerifyHostname("192.0.2.57"); err != nil {
		t.Fatalf("server certificate must cover the LAN address: %v", err)
	}
	// The device certificate file itself must be untouched: same serial.
	if deviceAfter := mustParsePEM(t, deviceCert); deviceBefore.SerialNumber.Cmp(deviceAfter.SerialNumber) != 0 {
		t.Fatalf("device certificate was re-issued: serial %s became %s",
			deviceBefore.SerialNumber, deviceAfter.SerialNumber)
	}

	// An address outside the SAN list must still be refused: re-signing
	// adds names, it must not disable verification.
	if err := mustParsePEM(t, filepath.Join(dir, ServerCertFile)).VerifyHostname("10.0.0.9"); err == nil {
		t.Fatal("a host outside the SAN list must fail verification")
	}

	if err := IssueServer(dir, nil); err == nil {
		t.Fatal("empty host list must fail")
	}
	if err := IssueServer(t.TempDir(), []string{"localhost"}); err == nil {
		t.Fatal("re-signing without a CA must fail")
	}
}

// TestEnsureCAIdempotent verifies the container-init variant provisions a PKI
// on first use and is a safe no-op afterwards (it must never rotate an
// existing CA or fail when re-run on every pod start).
func TestEnsureCAIdempotent(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureCA(dir, nil); err == nil {
		t.Fatal("ensure without server hosts must fail")
	}
	if err := EnsureCA(dir, []string{"localhost"}); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	ca1, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	// Re-running over an existing CA must succeed and leave it byte-identical.
	if err := EnsureCA(dir, []string{"localhost"}); err != nil {
		t.Fatalf("second ensure must be a no-op: %v", err)
	}
	ca2, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		t.Fatalf("re-read CA: %v", err)
	}
	if string(ca1) != string(ca2) {
		t.Fatal("EnsureCA rotated an existing CA")
	}
}

// mustParsePEM reads and parses a certificate PEM file.
func mustParsePEM(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	//nolint:gosec // path is inside the per-test temp directory
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert
}
