// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// credential_reuse_db_test.go verifies ADR-0009 credential-reuse
// detection: the same X.509 client certificate may not be bound to a
// second device, while distinct certificates remain allowed and the
// startup backfill is idempotent.

package registry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const reuseTestTenant = "00000000-0000-0000-0000-00000000d702"

// selfSignedPEM mints a fresh leaf certificate for the given common name
// and returns its PEM encoding. Each call yields a distinct key/cert.
func selfSignedPEM(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestX509CredentialReuseRejected binds one certificate to two devices:
// the first succeeds, the second must return ErrConflict. It then proves
// a distinct certificate is still accepted and the backfill is a no-op.
func TestX509CredentialReuseRejected(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin := NewAdmin(pool)

	const devA = "00000000-0000-0000-0000-00000000d721"
	const devB = "00000000-0000-0000-0000-00000000d722"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx,
			`DELETE FROM device_credential WHERE device_id IN ($1, $2)`, devA, devB)
		for _, id := range []string{devA, devB} {
			_, _ = pool.Exec(ctx, `DELETE FROM device WHERE id = $1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, reuseTestTenant)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'reuse-test')
		 ON CONFLICT (id) DO NOTHING`, reuseTestTenant); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ id, name string }{
		{devA, "reuse-a"}, {devB, "reuse-b"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state)
			 VALUES ($1, $2, $3, 'ACTIVE')
			 ON CONFLICT (id) DO NOTHING`, pair.id, reuseTestTenant, pair.name); err != nil {
			t.Fatal(err)
		}
	}

	sharedPEM := selfSignedPEM(t, "shared")
	if _, err := admin.CreateCredential(ctx, reuseTestTenant, devA, CredentialDTO{
		Type:             "X509",
		CredentialsID:    "reuse-cid-shared",
		CredentialsValue: sharedPEM,
		Status:           CredentialActive,
	}, "tester", "127.0.0.1"); err != nil {
		t.Fatalf("first binding must succeed: %v", err)
	}

	if _, err := admin.CreateCredential(ctx, reuseTestTenant, devB, CredentialDTO{
		Type:             "X509",
		CredentialsID:    "reuse-cid-shared-copy",
		CredentialsValue: sharedPEM,
		Status:           CredentialActive,
	}, "tester", "127.0.0.1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second binding of same certificate must be ErrConflict, got %v", err)
	}

	otherPEM := selfSignedPEM(t, "other")
	if _, err := admin.CreateCredential(ctx, reuseTestTenant, devB, CredentialDTO{
		Type:             "X509",
		CredentialsID:    "reuse-cid-other",
		CredentialsValue: otherPEM,
		Status:           CredentialActive,
	}, "tester", "127.0.0.1"); err != nil {
		t.Fatalf("distinct certificate must be accepted: %v", err)
	}

	filled, err := admin.BackfillCertFingerprints(ctx)
	if err != nil {
		t.Fatalf("backfill must not error: %v", err)
	}
	if filled != 0 {
		t.Fatalf("backfill must be idempotent once fingerprints are set, filled=%d", filled)
	}
}
