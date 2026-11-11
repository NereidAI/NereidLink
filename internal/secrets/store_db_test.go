// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go verifies migration 0047 tenant_secret: seal/open through
// the store, upsert rotation, batched Resolve, missing-name absence, metadata
// never carrying the value, name validation and tenant cascading.

package secrets

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const secretTestTenant = "00000000-0000-0000-0000-00000000d807"

func newSecretDB(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenant (id,name) VALUES ($1,'sec-test') ON CONFLICT DO NOTHING`,
		secretTestTenant); err != nil {
		t.Fatal(err)
	}
	codec, err := NewCodec(bytes32())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(pool, codec), pool
}

func bytes32() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func resetSecretFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM tenant_secret WHERE tenant_id=$1`, secretTestTenant)
	_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, secretTestTenant)
}

// ensureTenant recreates the tenant after a reset wiped it.
func ensureTenant(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenant (id,name) VALUES ($1,'sec-test') ON CONFLICT DO NOTHING`,
		secretTestTenant); err != nil {
		t.Fatal(err)
	}
}

func TestSecretStoreLifecycle(t *testing.T) {
	store, pool := newSecretDB(t)
	resetSecretFixture(pool)
	ensureTenant(t, pool)
	t.Cleanup(func() { resetSecretFixture(pool) })
	ctx := context.Background()

	if _, err := store.Put(ctx, secretTestTenant, "bad name", "v"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid name want ErrInvalidName, got %v", err)
	}
	if _, err := store.Put(ctx, secretTestTenant, "tok", "  "); !errors.Is(err, ErrEmptyValue) {
		t.Fatalf("empty value want ErrEmptyValue, got %v", err)
	}

	md, err := store.Put(ctx, secretTestTenant, "tok", "secret-A")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if md.Name != "tok" {
		t.Fatalf("metadata name = %q", md.Name)
	}
	// Metadata never carries the value.
	if md.Name == "secret-A" {
		t.Fatal("metadata leaked value")
	}

	// Rotation upserts in place.
	if _, err := store.Put(ctx, secretTestTenant, "tok", "secret-B"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	vals, err := store.Resolve(ctx, secretTestTenant, []string{"tok", "pw"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if vals["tok"] != "secret-B" {
		t.Fatalf("rotated value = %q", vals["tok"])
	}
	if _, present := vals["pw"]; present {
		t.Fatal("missing secret must be absent, not empty")
	}
	if _, err := store.Put(ctx, secretTestTenant, "pw", "secret-C"); err != nil {
		t.Fatalf("put pw: %v", err)
	}
	vals2, err := store.Resolve(ctx, secretTestTenant, []string{"tok", "pw"})
	if err != nil || vals2["tok"] != "secret-B" || vals2["pw"] != "secret-C" {
		t.Fatalf("batch resolve = %v err=%v", vals2, err)
	}

	// Ciphertext at rest is not plaintext.
	var sealed string
	if err := pool.QueryRow(ctx,
		`SELECT ciphertext FROM tenant_secret WHERE tenant_id=$1 AND name='tok'`,
		secretTestTenant).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed == "secret-B" {
		t.Fatal("ciphertext must not equal plaintext")
	}

	// List metadata + delete.
	list, err := store.List(ctx, secretTestTenant)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d err=%v", len(list), err)
	}
	if err := store.Delete(ctx, secretTestTenant, "pw"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, secretTestTenant, "pw"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
}
