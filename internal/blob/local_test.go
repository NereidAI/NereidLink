// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// local_test.go covers the directory blob backend: atomic writes, reads,
// ranged reads, metadata, deletion and signed-URL verification.

package blob

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// newTestLocal builds a localStore rooted in a temporary directory.
func newTestLocal(t *testing.T) *localStore {
	t.Helper()
	store, err := NewLocal(context.Background(), LocalConfig{
		Directory:  t.TempDir(),
		SigningKey: "test-signing-key",
		SignTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("new local store: %v", err)
	}
	return store
}

// TestLocalPutGetRoundTrip ensures bytes stored are read back intact along
// with the declared content type.
func TestLocalPutGetRoundTrip(t *testing.T) {
	store := newTestLocal(t)
	body := []byte("hello blob object")
	size, err := store.PutObject(context.Background(),
		"firmware/tenant-1/pkg-1", bytes.NewReader(body), int64(len(body)),
		"application/octet-stream")
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
	reader, err := store.GetObject(context.Background(), "firmware/tenant-1/pkg-1")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// TestLocalRangeRead verifies inclusive byte windows.
func TestLocalRangeRead(t *testing.T) {
	store := newTestLocal(t)
	if _, err := store.PutObject(context.Background(),
		"firmware/t/p", bytes.NewReader([]byte("0123456789")), 10,
		"application/octet-stream"); err != nil {
		t.Fatalf("put: %v", err)
	}
	reader, err := store.GetObjectRange(context.Background(), "firmware/t/p", 2, 4)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, _ := io.ReadAll(reader)
	if string(got) != "2345" {
		t.Fatalf("range body = %q, want 2345", got)
	}

	reader, err = store.GetObjectRange(context.Background(), "firmware/t/p", 8, 0)
	if err != nil {
		t.Fatalf("open-ended range: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, _ = io.ReadAll(reader)
	if string(got) != "89" {
		t.Fatalf("tail = %q, want 89", got)
	}
}

// TestLocalDelete verifies deletion removes the object and a second delete
// stays idempotent.
func TestLocalDelete(t *testing.T) {
	store := newTestLocal(t)
	if _, err := store.PutObject(context.Background(), "k",
		bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.DeleteObject(context.Background(), "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := store.StatObject(context.Background(), "k"); err != ErrObjectNotFound {
		t.Fatalf("stat after delete = %v, want ErrObjectNotFound", err)
	}
	if err := store.DeleteObject(context.Background(), "k"); err != nil {
		t.Fatalf("second delete must be idempotent: %v", err)
	}
}

// TestLocalRejectsKeyTraversal ensures keys cannot escape the root, both
// at the shared key validator and the local path mapping.
func TestLocalRejectsKeyTraversal(t *testing.T) {
	store := newTestLocal(t)
	if _, err := store.PutObject(context.Background(), "../escape",
		bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal("traversal key must be rejected")
	}
	if _, err := store.PutObject(context.Background(), "a/../../escape",
		bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal("nested traversal key must be rejected")
	}
}

// TestSignedURLRoundTrip verifies a presigned URL authenticates and that
// tampered signatures and expired URLs are refused.
func TestSignedURLRoundTrip(t *testing.T) {
	store := newTestLocal(t)
	signed, err := store.PresignGet(context.Background(), "firmware/t/p")
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	q := u.Query()
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	if !VerifyObjectURL("test-signing-key", q.Get("key"), exp, q.Get("sig")) {
		t.Fatal("fresh signed URL must verify")
	}
	if VerifyObjectURL("other-key", q.Get("key"), exp, q.Get("sig")) {
		t.Fatal("URL signed with another key must not verify")
	}
	if VerifyObjectURL("test-signing-key", "firmware/t/other", exp, q.Get("sig")) {
		t.Fatal("URL altered to name another key must not verify")
	}
	past := time.Now().Add(-time.Minute).Unix()
	sig := objectSignature(store.signKey, q.Get("key"), past)
	if VerifyObjectURL("test-signing-key", q.Get("key"), past, sig) {
		t.Fatal("expired URL must not verify")
	}
}

// TestLocalReservedKeySuffix rejects keys that collide with metadata files.
func TestLocalReservedKeySuffix(t *testing.T) {
	store := newTestLocal(t)
	if _, err := store.PutObject(context.Background(), "x"+localMetaSuffix,
		bytes.NewReader([]byte("x")), 1, ""); err == nil {
		t.Fatal("reserved suffix key must be rejected")
	}
}
