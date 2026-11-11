// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// codec.go holds the Codec: level selection, content-type/size gating,
// S2 and zstd encoding/decoding, the framed wire header and metrics.

package buscodec

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
)

// Frame magic and codec identifiers. The magic is the ASCII string
// "NL1"; it cannot appear at the start of an uncompressed protobuf
// Envelope (an Envelope begins with the field-1 wire tag, 0x0A), which
// is what makes magic detection unambiguous against older messages.
var frameMagic = []byte{0x4E, 0x4C, 0x31}

const (
	codecIDS2 = 1 + iota
	codecIDZstd
)

// DefaultMinBytes is the default size below which a message is not
// compressed. Compressing small frames usually inflates them and adds
// CPU for no bandwidth gain.
const DefaultMinBytes = 256

// Config parameterizes a Codec.
type Config struct {
	// Level is one of the Level* constants.
	Level string

	// MinBytes gates messages smaller than this; 0 uses DefaultMinBytes.
	MinBytes int
}

// Codec compresses and decompresses bus messages. A zero-value Codec
// must not be used; construct it with New. It is safe for concurrent
// use: the level switch is guarded and the underlying encoders are
// concurrency-safe.
type Codec struct {
	mu       sync.RWMutex
	level    string
	minBytes int
	zstdBal  *zstd.Encoder
	zstdMax  *zstd.Encoder
	zstdDec  *zstd.Decoder
	metrics  *metrics
}

// New constructs a Codec. Balanced and max each own a reusable zstd
// encoder so per-message allocations stay low; one shared decoder reads
// every zstd frame regardless of the encoder level.
func New(cfg Config) (*Codec, error) {
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	minBytes := cfg.MinBytes
	if minBytes <= 0 {
		minBytes = DefaultMinBytes
	}

	bal, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	max, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}

	return &Codec{
		level:    level,
		minBytes: minBytes,
		zstdBal:  bal,
		zstdMax:  max,
		zstdDec:  dec,
		metrics:  newMetrics(),
	}, nil
}

// RegisterMetrics publishes compression counters, gauges and latency
// histograms onto reg. It is typically called once from the composition
// root.
func (c *Codec) RegisterMetrics(reg prometheus.Registerer) error {
	return c.metrics.register(reg)
}

// Level returns the active level.
func (c *Codec) Level() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.level
}

// SetLevel switches the level at runtime. The change affects subsequent
// messages only; already stored messages keep their encoding and remain
// readable.
func (c *Codec) SetLevel(raw string) error {
	level, err := ParseLevel(raw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.level = level
	c.mu.Unlock()
	return nil
}

// MinBytes returns the size threshold.
func (c *Codec) MinBytes() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.minBytes
}

// result labels recorded by metrics.
const (
	resultEncoded        = "encoded"
	resultSkippedContent = "skipped_content"
	resultSkippedSize    = "skipped_size"
	resultNoGain         = "no_gain"
	resultDisabled       = "disabled"
)

// Encode returns the bus-ready form of raw. contentType drives the
// already-compressed skip rule. The input is returned unchanged when
// compression is off, the content type is incompressible, the message
// is below threshold or compression does not shrink it.
func (c *Codec) Encode(raw []byte, contentType string) []byte {
	start := time.Now()
	c.mu.RLock()
	level := c.level
	minBytes := c.minBytes
	c.mu.RUnlock()

	if level == LevelOff {
		c.metrics.observe(resultDisabled, len(raw), len(raw), start)
		return raw
	}
	if !CompressibleContentType(contentType) {
		c.metrics.observe(resultSkippedContent, len(raw), len(raw), start)
		return raw
	}
	if len(raw) < minBytes {
		c.metrics.observe(resultSkippedSize, len(raw), len(raw), start)
		return raw
	}

	compressed := c.encodeByLevel(level, raw)

	// Account for the header and only ship compression that actually
	// shrinks the message; a net inflate means compression lost.
	if len(compressed)+len(frameMagic)+1 >= len(raw) {
		c.metrics.observe(resultNoGain, len(raw), len(raw), start)
		return raw
	}

	framed := make([]byte, 0, len(frameMagic)+1+len(compressed))
	framed = append(framed, frameMagic...)
	framed = append(framed, codecByte(level))
	framed = append(framed, compressed...)
	c.metrics.observe(resultEncoded, len(raw), len(framed), start)
	return framed
}

// encodeByLevel compresses raw with the codec assigned to level.
func (c *Codec) encodeByLevel(level string, raw []byte) []byte {
	switch level {
	case LevelFast:
		return s2.Encode(nil, raw)
	case LevelMax:
		return c.zstdMax.EncodeAll(raw, nil)
	default:
		return c.zstdBal.EncodeAll(raw, nil)
	}
}

// codecByte maps a level to the frame codec id.
func codecByte(level string) byte {
	if level == LevelFast {
		return codecIDS2
	}
	return codecIDZstd
}

// Decode returns the original bytes of a message produced by Encode.
// Uncompressed input (no frame magic) is returned as-is, which covers
// both level=off producers and messages stored before compression was
// enabled.
func (c *Codec) Decode(raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, frameMagic) {
		return raw, nil
	}
	if len(raw) < len(frameMagic)+1 {
		return nil, errors.New("truncated buscodec frame")
	}

	codec := raw[len(frameMagic)]
	body := raw[len(frameMagic)+1:]
	start := time.Now()

	var out []byte
	var err error
	switch codec {
	case codecIDS2:
		out, err = s2.Decode(nil, body)
	case codecIDZstd:
		out, err = c.zstdDec.DecodeAll(body, nil)
	default:
		return nil, errors.New("unknown buscodec codec id")
	}
	c.metrics.observeDecode(time.Since(start))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// incompressiblePrefixes are media families that are already encoded;
// compressing them spends CPU for negligible size reduction.
var incompressiblePrefixes = []string{
	"image/", "audio/", "video/",
}

// incompressibleExactTypes are compressed application payloads.
var incompressibleExactTypes = map[string]struct{}{
	"application/gzip":            {},
	"application/x-gzip":          {},
	"application/zip":             {},
	"application/x-xz":            {},
	"application/x-7z-compressed": {},
	"application/x-bzip2":         {},
	"application/zstd":            {},
}

// CompressibleContentType reports whether a content type should be
// considered for compression. An empty/unknown type is treated as
// compressible because the common bus case (protobuf/json metadata)
// benefits from compression. Comparison is case-insensitive and
// ignores parameters after ';'.
func CompressibleContentType(contentType string) bool {
	media := strings.TrimSpace(strings.ToLower(strings.Split(contentType, ";")[0]))
	if media == "" {
		return true
	}
	for _, prefix := range incompressiblePrefixes {
		if strings.HasPrefix(media, prefix) {
			return false
		}
	}
	if _, blocked := incompressibleExactTypes[media]; blocked {
		return false
	}
	return true
}
