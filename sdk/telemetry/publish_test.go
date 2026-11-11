// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
)

// fakeMediaStore records offload calls and returns a canned key.
type fakeMediaStore struct {
	key         string
	err         error
	calls       int
	contentType string
	data        []byte
}

// PutMedia implements MediaStore.
func (f *fakeMediaStore) PutMedia(_ context.Context, _ Identity,
	contentType, _ string, data []byte) (string, error) {
	f.calls++
	f.contentType = contentType
	f.data = data
	return f.key, f.err
}

// TestPointForBlobMarker verifies a marked field is decoded, offloaded and
// turned into a blob-referencing string point.
func TestPointForBlobMarker(t *testing.T) {
	payload := []byte("PNG-bytes")
	store := &fakeMediaStore{key: "media/t/d/abc"}
	marker := []byte(`{"blob":true,"content_type":"image/png","data":"` +
		base64.StdEncoding.EncodeToString(payload) + `"}`)
	point, err := pointForRaw(context.Background(), "photo", marker,
		Identity{TenantID: "t", DeviceID: "d"}, store, nil, nil)
	if err != nil {
		t.Fatalf("pointForRaw: %v", err)
	}
	if store.calls != 1 || string(store.data) != string(payload) {
		t.Fatalf("offload not performed with payload: %d calls", store.calls)
	}
	if point.VString != BlobURIScheme+"media/t/d/abc" {
		t.Fatalf("v_string = %q", point.VString)
	}
	if point.ValueType != v1.ValueType_VALUE_TYPE_STRING {
		t.Fatalf("value type = %v", point.ValueType)
	}
}

// TestPointForBlobMarkerBase64Variants accepts URL-safe and unpadded base64.
func TestPointForBlobMarkerBase64Variants(t *testing.T) {
	payload := []byte{0xFB, 0xFF, 0x00, 0x10}
	cases := map[string]string{
		"std padded": base64.StdEncoding.EncodeToString(payload),
		"std raw":    base64.RawStdEncoding.EncodeToString(payload),
		"url raw":    base64.RawURLEncoding.EncodeToString(payload),
		"url padded": base64.URLEncoding.EncodeToString(payload),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeMediaStore{key: "k"}
			marker := []byte(`{"blob":true,"data":"` + encoded + `"}`)
			if _, err := pointForRaw(context.Background(), "m", marker,
				Identity{}, store, nil, nil); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(store.data) != string(payload) {
				t.Fatalf("payload mismatch: %x", store.data)
			}
		})
	}
}

// TestPointForBlobMarkerRejections covers marker validation failures.
func TestPointForBlobMarkerRejections(t *testing.T) {
	ctx := context.Background()
	id := Identity{}
	cases := map[string]string{
		"not blob":         `{"blob":false,"data":"AA=="}`,
		"missing data":     `{"blob":true}`,
		"bad base64":       `{"blob":true,"data":"%%%"}`,
		"empty payload":    `{"blob":true,"data":""}`,
		"malformed marker": `{not json`,
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeMediaStore{key: "k"}
			if _, err := pointForRaw(ctx, "m", []byte(frame), id, store, nil, nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("store missing", func(t *testing.T) {
		frame := []byte(`{"blob":true,"data":"AA=="}`)
		_, err := pointForRaw(ctx, "m", frame, id, nil, nil, nil)
		if err == nil || err.Error() != "blob storage not configured" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("offload failure propagated", func(t *testing.T) {
		store := &fakeMediaStore{err: errors.New("s3 down")}
		frame := []byte(`{"blob":true,"data":"AA=="}`)
		if _, err := pointForRaw(ctx, "m", frame, id, store, nil, nil); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestPointForRawPrimitives confirms ordinary field classes are unaffected
// by the blob work.
func TestPointForRawPrimitives(t *testing.T) {
	ctx := context.Background()
	cases := map[string]byte{
		`"text"`: '"',
		`true`:   't',
		`false`:  'f',
		`42.5`:   '4',
		`null`:   'n',
	}
	for frame := range cases {
		_, err := pointForRaw(ctx, "m", []byte(frame), Identity{},
			&fakeMediaStore{key: "k"}, nil, nil)
		if frame == "null" {
			if err == nil {
				t.Fatal("null must be rejected")
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", frame, err)
		}
	}
}
