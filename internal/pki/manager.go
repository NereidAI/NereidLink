// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// manager.go is the runtime trust manager for device mTLS. It owns the
// platform_ca table (built-in CA plus imported third-party CAs), builds
// the live client-certificate verification pool and issues device
// certificates with the built-in CA private key that stays on the file
// system. The pool is read per TLS handshake through
// GetConfigForClient so importing or disabling a CA in the UI takes
// effect without a restart.

package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CA statuses stored in platform_ca.
const (
	// CAStatusActive trusts the anchor.
	CAStatusActive = "ACTIVE"

	// CAStatusDisabled keeps the row but stops trusting it.
	CAStatusDisabled = "DISABLED"
)

var (
	// errNotCA means an imported certificate was not a CA certificate.
	errNotCA = errors.New("imported certificate is not a certificate authority")

	// errBuiltinImmutable rejects delete of the built-in anchor.
	errBuiltinImmutable = errors.New("the built-in CA cannot be deleted")

	// errNoBuiltinCA means UI issuance was requested before pki init.
	errNoBuiltinCA = errors.New("built-in CA is not initialized; run pki init")
)

// CARecord is one platform trust anchor.
type CARecord struct {
	// ID is the row id.
	ID string `json:"id"`

	// Label is the operator-assigned display name.
	Label string `json:"label"`

	// SubjectCN names the certificate subject.
	SubjectCN string `json:"subject_cn"`

	// IssuerCN names the certificate issuer.
	IssuerCN string `json:"issuer_cn"`

	// NotBefore is the certificate validity start (RFC3339 UTC), parsed
	// from the stored PEM rather than its own column.
	NotBefore string `json:"not_before"`

	// NotAfter is the certificate expiry (RFC3339 UTC), parsed from the
	// stored PEM rather than its own column.
	NotAfter string `json:"not_after"`

	// FingerprintSHA256 is the hex fingerprint of the CA DER.
	FingerprintSHA256 string `json:"fingerprint_sha256"`

	// PEM is the CA certificate PEM.
	PEM string `json:"pem"`

	// Builtin marks the CA created by pki init.
	Builtin bool `json:"builtin"`

	// Status is ACTIVE or DISABLED.
	Status string `json:"status"`

	// CreatedAt is RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// UpdatedAt is RFC3339 UTC.
	UpdatedAt string `json:"updated_at"`
}

// Manager is the platform CA trust manager.
type Manager struct {
	// pool is the platform database.
	pool *pgxpool.Pool

	// caDir holds the built-in CA key material and wrapping key.
	caDir string

	// encKey wraps stored device private keys.
	encKey []byte
}

// NewManager constructs the manager and ensures the private-key
// wrapping key exists under caDir.
func NewManager(pool *pgxpool.Pool, caDir string) (*Manager, error) {
	encKey, err := loadOrCreateKeyEncryptionKey(caDir)
	if err != nil {
		return nil, err
	}
	return &Manager{pool: pool, caDir: caDir, encKey: encKey}, nil
}

// parseCAPEM decodes and validates one CA certificate PEM.
func parseCAPEM(rawPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(rawPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: value is not a PEM certificate", ErrInvalidCA)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCA, err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCA, errNotCA)
	}
	return cert, nil
}

// fingerprint returns the hex SHA-256 fingerprint of certificate DER.
func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// caColumns is the shared SELECT column order for platform_ca.
const caColumns = `id::text, label, subject_cn, issuer_cn, fingerprint_sha256,
	pem, builtin, status,
	to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`

// scanCARow maps one platform_ca row into a CARecord. The validity
// window is not stored in its own column; it is parsed from the PEM so
// the console can show expiry without a migration. A PEM that no longer
// parses leaves the fields empty rather than failing the whole list.
func scanCARow(row pgx.Row) (CARecord, error) {
	var record CARecord
	err := row.Scan(&record.ID, &record.Label, &record.SubjectCN, &record.IssuerCN,
		&record.FingerprintSHA256, &record.PEM, &record.Builtin, &record.Status,
		&record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		return record, err
	}
	if cert, perr := parseCAPEM(record.PEM); perr == nil {
		record.NotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
		record.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	}
	return record, nil
}

// ListCAs returns every trust anchor, newest first.
func (m *Manager) ListCAs(ctx context.Context) ([]CARecord, error) {
	rows, err := m.pool.Query(ctx,
		`SELECT `+caColumns+` FROM platform_ca ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []CARecord{}
	for rows.Next() {
		record, err := scanCARow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ImportCA validates and stores a third-party trust anchor. The
// certificate must be a CA; duplicate fingerprints are rejected so a
// CA cannot be imported twice under different labels.
func (m *Manager) ImportCA(ctx context.Context, label, rawPEM string) (CARecord, error) {
	if strings.TrimSpace(label) == "" {
		return CARecord{}, errors.New("label is required")
	}
	cert, err := parseCAPEM(rawPEM)
	if err != nil {
		return CARecord{}, err
	}
	normalized := strings.TrimSpace(rawPEM)
	row := m.pool.QueryRow(ctx,
		`INSERT INTO platform_ca
		   (label, subject_cn, issuer_cn, fingerprint_sha256, pem, builtin)
		 VALUES ($1, $2, $3, $4, $5, false)
		 RETURNING `+caColumns,
		label, cert.Subject.CommonName, cert.Issuer.CommonName,
		fingerprint(cert), normalized)
	record, err := scanCARow(row)
	if err != nil {
		return CARecord{}, mapCAError(err)
	}
	return record, nil
}

// SyncBuiltinCA upserts the built-in CA read from caFile. It is called
// at startup so the file-created CA is trusted through the same pool as
// imported anchors. PEM content and fingerprint come from the file; the
// label of an existing row is preserved.
func (m *Manager) SyncBuiltinCA(ctx context.Context, caFile string) error {
	rawPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-configured trusted CA file path
	if err != nil {
		return fmt.Errorf("read built-in CA: %w", err)
	}
	cert, err := parseCAPEM(string(rawPEM))
	if err != nil {
		return err
	}
	_, err = m.pool.Exec(ctx,
		`INSERT INTO platform_ca
		   (label, subject_cn, issuer_cn, fingerprint_sha256, pem, builtin)
		 VALUES ('Built-in Device CA', $1, $2, $3, $4, true)
		 ON CONFLICT (fingerprint_sha256)
		 DO UPDATE SET pem = EXCLUDED.pem,
		               subject_cn = EXCLUDED.subject_cn,
		               issuer_cn = EXCLUDED.issuer_cn,
		               updated_at = now()`,
		cert.Subject.CommonName, cert.Issuer.CommonName,
		fingerprint(cert), strings.TrimSpace(string(rawPEM)))
	return mapCAError(err)
}

// SetCAStatus enables or disables a trust anchor. Disabling takes
// effect on the next handshake; already established sessions are not
// dropped.
func (m *Manager) SetCAStatus(ctx context.Context, id, status string) error {
	if status != CAStatusActive && status != CAStatusDisabled {
		return errors.New("status must be ACTIVE or DISABLED")
	}
	tag, err := m.pool.Exec(ctx,
		`UPDATE platform_ca SET status = $1, updated_at = now() WHERE id = $2`,
		status, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCANotFound
	}
	return nil
}

// DeleteCA removes a trust anchor. The built-in CA is immutable and
// rejected before any row is touched.
func (m *Manager) DeleteCA(ctx context.Context, id string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var builtin bool
	if err := tx.QueryRow(ctx,
		`SELECT builtin FROM platform_ca WHERE id = $1 FOR UPDATE`, id).Scan(&builtin); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCANotFound
		}
		return err
	}
	if builtin {
		return errBuiltinImmutable
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform_ca WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// activeCAs returns ACTIVE anchor records.
func (m *Manager) activeCAs(ctx context.Context) ([]CARecord, error) {
	rows, err := m.pool.Query(ctx,
		`SELECT `+caColumns+` FROM platform_ca WHERE status = 'ACTIVE'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []CARecord{}
	for rows.Next() {
		record, err := scanCARow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ActivePool returns a certificate pool of every ACTIVE trust anchor.
func (m *Manager) ActivePool(ctx context.Context) (*x509.CertPool, error) {
	records, err := m.activeCAs(ctx)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	for _, record := range records {
		cert, err := parseCAPEM(record.PEM)
		if err != nil {
			return nil, fmt.Errorf("active CA %s unparseable: %w", record.ID, err)
		}
		pool.AddCert(cert)
	}
	return pool, nil
}

// ActiveBundle returns the concatenated PEM of every ACTIVE CA.
func (m *Manager) ActiveBundle(ctx context.Context) (string, error) {
	records, err := m.activeCAs(ctx)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, record := range records {
		builder.WriteString(strings.TrimSpace(record.PEM))
		builder.WriteString("\n")
	}
	return builder.String(), nil
}

// IssueDevice generates a P-256 key, signs a client certificate with
// the built-in CA key from the file system, and returns the certificate
// PEM, the private key PEM in plaintext and the built-in CA PEM. The
// certificate common name is cn and its lifetime ends at notAfter.
func (m *Manager) IssueDevice(ctx context.Context, cn string, notAfter time.Time,
) (certPEM, keyPEM, caPEM []byte, err error) {
	if strings.TrimSpace(cn) == "" {
		return nil, nil, nil, errors.New("device certificate common name is required")
	}
	caCert, _, caKey, err := loadCA(m.caDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, errNoBuiltinCA
		}
		return nil, nil, nil, err
	}
	if notAfter.IsZero() {
		notAfter = time.Now().Add(defaultDeviceValidity)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate device key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, fmt.Errorf("create device certificate: %w", err)
	}
	keyBlock, err := marshalECPrivateKeyPEM(key)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(keyBlock)
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	return certPEM, keyPEM, caPEM, nil
}

// EncryptPrivateKey seals a device private key PEM for extra storage.
func (m *Manager) EncryptPrivateKey(plaintext []byte) (string, error) {
	return encryptPrivateKey(m.encKey, plaintext)
}

// DecryptPrivateKey opens a sealed device private key PEM.
func (m *Manager) DecryptPrivateKey(blob string) ([]byte, error) {
	return decryptPrivateKey(m.encKey, blob)
}

// ServerTLSConfigDynamic builds the TLS config for the MQTT TLS
// listener. The server key pair is loaded once; the client-verification
// pool is rebuilt from the database on each handshake so CA changes in
// the console take effect immediately. requireClientCert selects
// strict mTLS versus optional client certificates (MQTTS default).
func (m *Manager) ServerTLSConfigDynamic(certFile, keyFile string, requireClientCert bool) (*tls.Config, error) {
	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	clientAuth := tls.VerifyClientCertIfGiven
	if requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   clientAuth,
		MinVersion:   tls.VersionTLS12,
		GetConfigForClient: func(_ *tls.ClientHelloInfo) (*tls.Config, error) {
			pool, err := m.ActivePool(context.Background())
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				Certificates: []tls.Certificate{serverCert},
				ClientAuth:   clientAuth,
				ClientCAs:    pool,
				MinVersion:   tls.VersionTLS12,
			}, nil
		},
		ClientCAs: x509.NewCertPool(),
	}, nil
}
