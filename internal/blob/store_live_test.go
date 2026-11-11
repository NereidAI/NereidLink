// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_live_test.go exercises the S3 blob store against a real S3-compatible
// endpoint (SeaweedFS/MinIO). It is opt-in via NL_BLOB_ENDPOINT so normal
// unit runs need no object store. Example:
//
//	NL_BLOB_ENDPOINT=10.0.0.5:8333 \
//	NL_BLOB_ACCESS_KEY=x NL_BLOB_SECRET_KEY=y NL_BLOB_BUCKET=nl-test \
//	NL_BLOB_PUBLIC_ENDPOINT=s3.example.com go test ./internal/blob/...

package blob

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func liveStore(t *testing.T) *Store {
	t.Helper()
	ep := os.Getenv("NL_BLOB_ENDPOINT")
	if ep == "" {
		t.Skip("set NL_BLOB_ENDPOINT to run live blob tests")
	}
	cfg := Config{
		Endpoint:       ep,
		PublicEndpoint: os.Getenv("NL_BLOB_PUBLIC_ENDPOINT"),
		Region:         "us-east-1",
		AccessKey:      os.Getenv("NL_BLOB_ACCESS_KEY"),
		SecretKey:      os.Getenv("NL_BLOB_SECRET_KEY"),
		Bucket:         envOr("NL_BLOB_BUCKET", "nereidlink-test"),
		PresignTTL:     5 * time.Minute,
	}
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new blob store: %v", err)
	}
	return s
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestLivePutGetPresignDelete(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	key := "test/live-" + time.Now().Format("150405.000000") + ".bin"
	body := []byte("blob-live-roundtrip-" + time.Now().String())

	size, err := s.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if size != int64(len(body)) {
		t.Fatalf("stored size=%d want %d", size, len(body))
	}
	t.Cleanup(func() { _ = s.DeleteObject(context.Background(), key) })

	got, err := s.GetObject(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	rb, _ := io.ReadAll(got)
	_ = got.Close()
	if !bytes.Equal(rb, body) {
		t.Fatalf("roundtrip mismatch: %q vs %q", rb, body)
	}

	st, _, err := s.StatObject(ctx, key)
	if err != nil || st != int64(len(body)) {
		t.Fatalf("stat size=%d err=%v", st, err)
	}

	urlStr, err := s.PresignGet(ctx, key)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if !strings.Contains(urlStr, "X-Amz-Signature=") || !strings.Contains(urlStr, key) {
		t.Fatalf("presigned url malformed: %s", urlStr)
	}
	t.Logf("presigned: %s", urlStr)

	if err := s.DeleteObject(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := s.StatObject(ctx, key); err == nil {
		t.Fatal("object still present after delete")
	}
}

func TestLiveSafeKey(t *testing.T) {
	for _, bad := range []string{"", "a/../b", "..", "a/../../x"} {
		if err := safeKey(bad); err == nil {
			t.Errorf("key %q must be rejected", bad)
		}
	}
	for _, good := range []string{"a/b.bin", "firmware/t/x"} {
		if err := safeKey(good); err != nil {
			t.Errorf("key %q must pass: %v", good, err)
		}
	}
}
