// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// processor_buscodec_test.go guards the regression where the rule
// processor was the one telemetry consumer without a buscodec decoder:
// with ADR-0003 compression enabled it unmarshaled compressed bytes as
// protobuf, logged "rule processor bad envelope", and acked-dropped the
// message, so chains silently stopped firing for every frame above the
// compression size threshold.

package rule

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/buscodec"
)

// staticDecoder is a deterministic FrameDecoder for tests.
type staticDecoder struct {
	out []byte
	err error
}

func (d staticDecoder) Decode(_ []byte) ([]byte, error) {
	return d.out, d.err
}

func TestProcessorDecodeCompressedEnvelope(t *testing.T) {
	p := NewProcessor(nil, nil, Deps{}, nil, ProcessorTuning{})
	codec, err := buscodec.New(buscodec.Config{
		Level:    buscodec.LevelBalanced,
		MinBytes: 1,
	})
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	p.SetFrameDecoder(codec)

	env := &v1.Envelope{
		TenantId: "tenant-A",
		DeviceId: "dev-1",
		Type:     v1.MsgType_MSG_TYPE_TELEMETRY,
		Payload:  []byte(strings.Repeat("compression-regression-fixture-value-", 20)),
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compressed := codec.Encode(raw, "application/protobuf")
	if string(compressed[:3]) != "NL1" {
		t.Fatal("test setup: frame was not compressed")
	}

	got, err := p.decodeBusFrame(compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var back v1.Envelope
	if err := proto.Unmarshal(got, &back); err != nil {
		t.Fatalf("decoded bytes are not an envelope: %v", err)
	}
	if back.TenantId != "tenant-A" || back.DeviceId != "dev-1" {
		t.Fatalf("envelope lost in compression round-trip: tenant=%q device=%q",
			back.TenantId, back.DeviceId)
	}
}

func TestProcessorDecodePassthroughWithoutDecoder(t *testing.T) {
	p := NewProcessor(nil, nil, Deps{}, nil, ProcessorTuning{})
	raw := []byte{0x0a}
	got, err := p.decodeBusFrame(raw)
	if err != nil {
		t.Fatalf("nil decoder must be passthrough: %v", err)
	}
	if len(got) != 1 || got[0] != 0x0a {
		t.Fatalf("raw bytes altered without a decoder: %v", got)
	}
}

func TestProcessorDecodeSurfacesDecoderError(t *testing.T) {
	p := NewProcessor(nil, nil, Deps{}, nil, ProcessorTuning{})
	wantErr := errors.New("boom")
	p.SetFrameDecoder(staticDecoder{err: wantErr})
	if _, err := p.decodeBusFrame([]byte("NL1xx")); !errors.Is(err, wantErr) {
		t.Fatalf("want wrapped decoder error, got %v", err)
	}
}
