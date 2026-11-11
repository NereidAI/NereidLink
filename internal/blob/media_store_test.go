// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"errors"
	"io"
	"testing"

	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// fakeSink records the last PutObject call.
type fakeSink struct {
	key         string
	contentType string
	data        []byte
	err         error
}

// PutObject implements MediaSink.
func (f *fakeSink) PutObject(_ context.Context, key string, r io.Reader,
	_ int64, contentType string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.key = key
	f.contentType = contentType
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.data = data
	return int64(len(data)), nil
}

// TestMediaStorePutHappyPath verifies key generation and payload storage.
func TestMediaStorePutHappyPath(t *testing.T) {
	sink := &fakeSink{}
	store := NewMediaStore(sink, 0, nil)
	data := []byte("image-bytes")
	key, err := store.PutMedia(context.Background(),
		sdktelemetry.Identity{TenantID: "tn", DeviceID: "dv"},
		"image/jpeg", "cam.jpg", data)
	if err != nil {
		t.Fatalf("PutMedia: %v", err)
	}
	tenant, device, ok := MediaKeyOwner(key)
	if !ok || tenant != "tn" || device != "dv" {
		t.Fatalf("key owner = (%s,%s,%v)", tenant, device, ok)
	}
	if sink.contentType != "image/jpeg" || string(sink.data) != string(data) {
		t.Fatal("payload or type mismatch")
	}
}

// TestMediaStoreSizeLimit rejects oversized data and does not call the sink.
func TestMediaStoreSizeLimit(t *testing.T) {
	sink := &fakeSink{}
	store := NewMediaStore(sink, 4, nil)
	_, err := store.PutMedia(context.Background(), sdktelemetry.Identity{},
		"image/png", "", []byte("12345"))
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

// TestMediaStoreTypePolicy verifies prefix and exact matching plus empty.
func TestMediaStoreTypePolicy(t *testing.T) {
	store := NewMediaStore(&fakeSink{}, 1000, []string{"image/", "video/mp4"})
	cases := map[string]bool{
		"image/jpeg":      true,
		"image/png":       true,
		"video/mp4":       true,
		"video/webm":      false,
		"text/plain":      false,
		"application/zip": false,
		"":                true,
	}
	for contentType, allowed := range cases {
		if got := store.ContentTypeAllowed(contentType); got != allowed {
			t.Fatalf("ContentTypeAllowed(%q) = %v want %v", contentType, got, allowed)
		}
	}
}

// TestMediaKeyOwnerMalformed rejects non-media and malformed keys.
func TestMediaKeyOwnerMalformed(t *testing.T) {
	for _, key := range []string{
		"firmware/t/d/x",
		"media/t",
		"media/t/d",
		"media/t/d/e/f",
	} {
		if _, _, ok := MediaKeyOwner(key); ok {
			t.Fatalf("%q accepted", key)
		}
	}
}
