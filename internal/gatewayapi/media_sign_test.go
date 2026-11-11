// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/blob"
)

// testBlobStore exposes just the PutObject capability needed to seed an
// object, since blob backends carry no exported concrete type.
type testBlobStore interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
}

// newSignTestHub builds a Hub with a local blob backend in a temp dir.
func newSignTestHub(t *testing.T) (*Hub, testBlobStore) {
	t.Helper()
	store, err := blob.NewLocal(context.Background(), blob.LocalConfig{
		Directory:  t.TempDir(),
		SigningKey: "sign-secret",
		SignTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return &Hub{blobs: store, blobSignKey: "sign-secret"}, store
}

// TestSignMediaBlob returns a verifiable signed URL for a valid key.
func TestSignMediaBlob(t *testing.T) {
	hub, store := newSignTestHub(t)
	key := blob.MediaKey("tenant-a", "dev-1", "obj-1")
	if _, err := store.PutObject(context.Background(), key,
		strings.NewReader("data"), 4, "image/jpeg"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/tenant-a/blob/sign?key="+url.QueryEscape(key), nil)
	req.SetPathValue("tenant", "tenant-a")
	rec := httptest.NewRecorder()
	hub.handleSignMediaBlob(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp signedBlobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	q := u.Query()
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("exp: %v", err)
	}
	if !blob.VerifyObjectURL("sign-secret", q.Get("key"), exp, q.Get("sig")) {
		t.Fatal("returned URL signature invalid")
	}
	if resp.Size != 4 || resp.ContentType != "image/jpeg" {
		t.Fatalf("meta = size %d type %q", resp.Size, resp.ContentType)
	}
}

// TestSignMediaBlobRejectsCrossTenant refuses keys outside the path tenant.
func TestSignMediaBlobRejectsCrossTenant(t *testing.T) {
	hub, _ := newSignTestHub(t)
	key := blob.MediaKey("tenant-b", "dev-1", "obj-1")
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/tenant-a/blob/sign?key="+url.QueryEscape(key), nil)
	req.SetPathValue("tenant", "tenant-a")
	rec := httptest.NewRecorder()
	hub.handleSignMediaBlob(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

// TestSignMediaBlobRejectsNonMediaKey refuses firmware/other keys.
func TestSignMediaBlobRejectsNonMediaKey(t *testing.T) {
	hub, _ := newSignTestHub(t)
	req := httptest.NewRequest(http.MethodGet,
		"/v1/tenants/tenant-a/blob/sign?key="+url.QueryEscape("firmware/tenant-a/x"), nil)
	req.SetPathValue("tenant", "tenant-a")
	rec := httptest.NewRecorder()
	hub.handleSignMediaBlob(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
