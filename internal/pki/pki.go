// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package pki manages the private PKI backing device mTLS (ADR-0009).
// It deliberately uses only the Go standard library: an ECDSA P-256 CA
// signs short-lived ECDSA device and server certificates. The package
// provides both key/cert generation for the CLI and the TLS config used
// by the MQTT TLS listener.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Standard file names inside a PKI directory.
const (
	// CAKeyFile is the PEM-encoded CA private key.
	CAKeyFile = "ca-key.pem"

	// CACertFile is the PEM-encoded CA certificate.
	CACertFile = "ca.pem"

	// ServerKeyFile is the PEM-encoded server private key.
	ServerKeyFile = "server-key.pem"

	// ServerCertFile is the PEM-encoded server certificate.
	ServerCertFile = "server.pem"

	// caValidity bounds the generated CA lifetime.
	caValidity = 10 * 365 * 24 * time.Hour

	// serverValidity bounds the generated server certificate lifetime.
	serverValidity = 365 * 24 * time.Hour

	// defaultDeviceValidity bounds generated device certificates.
	defaultDeviceValidity = 365 * 24 * time.Hour
)

var (
	// errSerialFailure means the CSPRNG could not issue a serial number.
	errSerialFailure = errors.New("failed to generate certificate serial")

	// errMissingHost means a server certificate had no SAN.
	errMissingHost = errors.New("at least one server host or IP is required")
)

// writePEM encodes a DER blob or key to path with 0600 permissions. The
// write goes to a temporary file in the same directory followed by an atomic
// rename, so a concurrent reader on the shared RWX volume (or a process
// crashing mid-write) never observes a truncated PEM.
func writePEM(path string, block *pem.Block) error {
	data := pem.EncodeToMemory(block)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pki-write-*")
	if err != nil {
		return fmt.Errorf("create temp pem: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// marshalECPrivateKeyPEM encodes an ECDSA private key as SEC1 PEM.
func marshalECPrivateKeyPEM(key *ecdsa.PrivateKey) (*pem.Block, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}, nil
}

// newSerial returns a random 128-bit certificate serial number.
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSerialFailure, err)
	}
	return serial, nil
}

// InitCA creates a self-signed CA plus a server certificate inside dir.
// Existing files are left untouched: reusing an old CA is preferred over
// silently rotating it, which would invalidate every issued device cert.
func InitCA(dir string, serverHosts []string) error {
	if len(serverHosts) == 0 {
		return errMissingHost
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pki directory: %w", err)
	}

	caKeyPath := filepath.Join(dir, CAKeyFile)
	caCertPath := filepath.Join(dir, CACertFile)
	if _, err := os.Stat(caCertPath); err == nil {
		return fmt.Errorf("CA already exists at %s; to change the server certificate "+
			"hosts run \"nereidlink pki server --hosts ...\" instead of rotating the CA", caCertPath)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "NereidLink Device CA",
			Organization: []string{"NereidLink"},
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create CA certificate: %w", err)
	}
	if err := writePEM(caKeyPath, mustMarshalECKey(caKey)); err != nil {
		return err
	}
	if err := writePEM(caCertPath, &pem.Block{Type: "CERTIFICATE", Bytes: caDER}); err != nil {
		return err
	}

	caCert := &template
	return issueLeaf("NereidLink MQTT", serverHosts, caCert, caKey,
		filepath.Join(dir, ServerKeyFile), filepath.Join(dir, ServerCertFile),
		serverValidity, x509.ExtKeyUsageServerAuth)
}

// EnsureCA idempotently guarantees a CA and MQTT server certificate exist in
// dir. It creates them on first use (e.g. a fresh PersistentVolume) and is a
// no-op when the CA is already present, so it is safe to run from a container
// init step on every start. Unlike InitCA it never fails on an existing CA.
func EnsureCA(dir string, serverHosts []string) error {
	if len(serverHosts) == 0 {
		return errMissingHost
	}
	return withDirLock(dir, func() error {
		// Re-check under the lock: a peer pod sharing the RWX volume may
		// have provisioned the CA while this pod waited for the lock.
		if _, err := os.Stat(filepath.Join(dir, CACertFile)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat existing CA: %w", err)
		}
		return InitCA(dir, serverHosts)
	})
}

// mustMarshalECKey marshals an EC key, failing only on programmer error.
func mustMarshalECKey(key *ecdsa.PrivateKey) *pem.Block {
	block, err := marshalECPrivateKeyPEM(key)
	if err != nil {
		panic(err)
	}
	return block
}

// loadCA reads the CA key pair from dir.
func loadCA(dir string) (*x509.Certificate, []byte, *ecdsa.PrivateKey, error) {
	//nolint:gosec // operator-selected PKI directory
	certPEM, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, nil, errors.New("CA cert is not valid PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	//nolint:gosec // operator-selected PKI directory
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, nil, errors.New("CA key is not valid PEM")
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return caCert, block.Bytes, caKey, nil
}

// issueLeaf creates and writes a leaf certificate signed by the CA.
func issueLeaf(commonName string, hosts []string,
	caCert *x509.Certificate, caKey *ecdsa.PrivateKey,
	keyPath, certPath string, validity time.Duration,
	extUsage x509.ExtKeyUsage) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{extUsage},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create leaf certificate: %w", err)
	}
	if err := writePEM(keyPath, mustMarshalECKey(key)); err != nil {
		return err
	}
	return writePEM(certPath, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// IssueServer re-signs only the MQTT server certificate from the existing
// CA, keeping every previously issued device certificate valid. Operators
// need this whenever the listener becomes reachable under a name or address
// that the current certificate does not cover: a client connecting to, say,
// a LAN IP fails hostname verification with
// ERR_TLS_CERT_ALTNAME_INVALID, and regenerating the CA would invalidate
// every registered X509 credential.
func IssueServer(dir string, serverHosts []string) error {
	if len(serverHosts) == 0 {
		return errMissingHost
	}
	return withDirLock(dir, func() error {
		caCert, _, caKey, err := loadCA(dir)
		if err != nil {
			return err
		}
		return issueLeaf("NereidLink MQTT", serverHosts, caCert, caKey,
			filepath.Join(dir, ServerKeyFile), filepath.Join(dir, ServerCertFile),
			serverValidity, x509.ExtKeyUsageServerAuth)
	})
}

// IssueDevice signs a client certificate for cn (normally the credential
// id) and writes its key and cert PEM files.
func IssueDevice(caDir, cn, keyPath, certPath string, validity time.Duration) error {
	if cn == "" {
		return errors.New("device certificate common name is required")
	}
	if validity == 0 {
		validity = defaultDeviceValidity
	}
	caCert, _, caKey, err := loadCA(caDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	return issueLeaf(cn, []string{}, caCert, caKey,
		keyPath, certPath, validity, x509.ExtKeyUsageClientAuth)
}

// IssueDeviceMaterial signs a client certificate for cn in memory and
// returns its PEM certificate and PEM private key without writing files.
// It backs UI-driven issuance: the certificate is persisted on the
// credential while the private key is returned to the operator exactly
// once. notAfter zero means the default device validity; the caller may
// bind it to the credential expires_at.
func IssueDeviceMaterial(caDir, cn string, notAfter time.Time) ([]byte, []byte, error) {
	if cn == "" {
		return nil, nil, errors.New("device certificate common name is required")
	}
	if notAfter.IsZero() {
		notAfter = time.Now().Add(defaultDeviceValidity)
	}
	caCert, _, caKey, err := loadCA(caDir)
	if err != nil {
		return nil, nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate device key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create device certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBlock, err := marshalECPrivateKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, pem.EncodeToMemory(keyBlock), nil
}

// ServerTLSConfig builds the TLS configuration for the MQTT TLS listener.
//
// By default client certificates are optional: devices authenticate with
// username/password (TOKEN/PASSWORD/JWT) over the encrypted channel, and
// X509 credentials present a client certificate verified when given.
// Setting requireClientCert to true switches to strict mTLS, under which
// every connection must present a certificate chained to clientCAFile.
func ServerTLSConfig(certFile, keyFile, clientCAFile string, requireClientCert bool) (*tls.Config, error) {
	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	//nolint:gosec // operator-configured trusted CA file path
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("client CA file contains no certificate")
	}
	clientAuth := tls.VerifyClientCertIfGiven
	if requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   clientAuth,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
