// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package buscodec

import (
	"strings"
	"testing"
)

// TestRoundTrip verifies every non-off level compresses and decodes
// back to the original bytes for a payload large enough to shrink.
func TestRoundTrip(t *testing.T) {
	// Highly repetitive 4KB payload: every codec should shrink it and
	// clear the 256-byte threshold.
	raw := []byte(strings.Repeat("NereidLink-bus-compression-", 120))

	for _, level := range []string{LevelFast, LevelBalanced, LevelMax} {
		t.Run(level, func(t *testing.T) {
			c, err := New(Config{Level: level})
			if err != nil {
				t.Fatalf("new codec: %v", err)
			}
			framed := c.Encode(raw, "application/protobuf")
			if len(framed) >= len(raw) {
				t.Fatalf("level %s did not shrink: %d >= %d",
					level, len(framed), len(raw))
			}
			got, err := c.Decode(framed)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(got) != string(raw) {
				t.Fatal("round trip mismatch")
			}
		})
	}
}

// TestOffAndPassthrough confirms off leaves bytes unchanged and Decode
// accepts uncompressed input.
func TestOffAndPassthrough(t *testing.T) {
	c, err := New(Config{Level: LevelOff})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Repeat("x", 500))
	if got := c.Encode(raw, "application/json"); string(got) != string(raw) {
		t.Fatal("off must not modify bytes")
	}
	got, err := c.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatal("decode passthrough mismatch")
	}
}

// TestSizeThreshold ensures small messages are not compressed.
func TestSizeThreshold(t *testing.T) {
	c, err := New(Config{Level: LevelBalanced, MinBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	small := []byte(strings.Repeat("a", 100))
	if got := c.Encode(small, "application/protobuf"); len(got) != len(small) {
		t.Fatal("message below threshold must pass through")
	}
}

// TestContentTypeSkip checks incompressible media bypass compression.
func TestContentTypeSkip(t *testing.T) {
	c, err := New(Config{Level: LevelMax})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Repeat("img", 200))
	cases := []string{
		"image/png", "image/jpeg; charset=binary",
		"audio/mpeg", "video/mp4", "application/gzip",
		"application/zip",
	}
	for _, ct := range cases {
		if got := c.Encode(raw, ct); len(got) != len(raw) {
			t.Fatalf("content type %q must skip compression", ct)
		}
	}

	// Compressible types.
	for _, ct := range []string{"", "application/json", "application/protobuf", "text/plain"} {
		if !CompressibleContentType(ct) {
			t.Fatalf("content type %q expected compressible", ct)
		}
	}
}

// TestSetLevel verifies the runtime level switch including invalid input.
func TestSetLevel(t *testing.T) {
	c, err := New(Config{Level: LevelOff})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetLevel("MAX"); err != nil {
		t.Fatalf("set level: %v", err)
	}
	if c.Level() != LevelMax {
		t.Fatalf("level = %s, want max", c.Level())
	}
	if err := c.SetLevel("bogus"); err != ErrInvalidLevel {
		t.Fatalf("err = %v, want ErrInvalidLevel", err)
	}
}

// TestCrossCodecDecode confirms a single decoder reads frames produced
// by either codec, mirroring the consumer side across levels.
func TestCrossCodecDecode(t *testing.T) {
	raw := []byte(strings.Repeat("cross-codec-", 100))

	producerFast, _ := New(Config{Level: LevelFast})
	producerMax, _ := New(Config{Level: LevelMax})
	consumer, _ := New(Config{Level: LevelOff})

	for _, framed := range [][]byte{
		producerFast.Encode(raw, "application/protobuf"),
		producerMax.Encode(raw, "application/protobuf"),
	} {
		got, err := consumer.Decode(framed)
		if err != nil {
			t.Fatalf("cross decode: %v", err)
		}
		if string(got) != string(raw) {
			t.Fatal("cross codec mismatch")
		}
	}
}
