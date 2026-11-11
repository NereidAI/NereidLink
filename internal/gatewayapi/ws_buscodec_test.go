// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ws_buscodec_test.go guards the regression where compressed telemetry
// envelopes reached only the persistence consumer: the WebSocket hub
// unmarshaled raw bus bytes without the shared buscodec decoder, so once
// ADR-0003 compression was enabled every frame above the size threshold
// failed to parse in routeTelemetry and was silently dropped (new or
// large measurements never appeared live, while REST after navigation
// showed them because the DB writer decoded correctly).

package gatewayapi

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/buscodec"
)

// repeated builds a large compressible string so the codec actually
// emits a compressed frame (small payloads are skipped as no gain).
func repeated(part string, n int) string {
	return strings.Repeat(part, n)
}

func TestRouteTelemetryDecodesCompressedEnvelope(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	codec, err := buscodec.New(buscodec.Config{
		Level:    buscodec.LevelBalanced,
		MinBytes: 1,
	})
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	hub.SetFrameDecoder(codec)

	const tenant, dev = "tenant-A", "dev-1"
	client := newTestClient()
	client.subscribe("sub1", ResourceTelemetry, tenant, []string{dev}, false)
	hub.clients[client] = struct{}{}

	// A long, highly compressible string both clears the compression
	// size/no-gain gates and models exactly the large string measurement
	// that exposed the bug.
	point := &v1.TelemetryPoint{
		MeasurementKey: "NewlySeenLabel",
		VString:        repeated("compression-regression-fixture-value-", 20),
		Quality:        "GOOD",
		ValueType:      v1.ValueType_VALUE_TYPE_STRING,
	}
	payload, err := proto.Marshal(point)
	if err != nil {
		t.Fatalf("marshal point: %v", err)
	}
	env := &v1.Envelope{
		Id:          "env-compressed-1",
		TenantId:    tenant,
		DeviceId:    dev,
		Type:        v1.MsgType_MSG_TYPE_TELEMETRY,
		ContentType: "application/protobuf",
		Payload:     payload,
		EventTime:   1_760_000_000_000,
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	compressed := codec.Encode(raw, env.ContentType)
	if string(compressed[:3]) != "NL1" {
		t.Fatal("test setup: frame was not compressed")
	}

	hub.routeTelemetry(tenant, compressed)

	frame := drainFrame(t, client)
	if frame.Type != ResourceTelemetry {
		t.Fatalf("frame type=%q want telemetry", frame.Type)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok || data["measurement_key"] != "NewlySeenLabel" {
		t.Fatalf("compressed frame not delivered intact: %#v", frame.Data)
	}
}

func TestRouteTelemetryPassthroughWithoutDecoder(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	const tenant, dev = "tenant-A", "dev-1"
	client := newTestClient()
	client.subscribe("sub1", ResourceTelemetry, tenant, []string{dev}, false)
	hub.clients[client] = struct{}{}

	env := &v1.Envelope{
		TenantId: tenant,
		DeviceId: dev,
		Type:     v1.MsgType_MSG_TYPE_TELEMETRY,
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hub.routeTelemetry(tenant, raw)

	// With no decoder wired (compression off), raw protobuf must still
	// route as before.
	frame := drainFrame(t, client)
	if frame.Type != ResourceTelemetry {
		t.Fatalf("frame type=%q want telemetry", frame.Type)
	}
}

func TestRouteTelemetryDropsCorruptCompressedFrame(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	codec, err := buscodec.New(buscodec.Config{
		Level:    buscodec.LevelBalanced,
		MinBytes: 1,
	})
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	hub.SetFrameDecoder(codec)

	const tenant, dev = "tenant-A", "dev-1"
	client := newTestClient()
	client.subscribe("sub1", ResourceTelemetry, tenant, []string{dev}, false)
	hub.clients[client] = struct{}{}

	// Valid magic/codec header but a truncated zstd body.
	hub.routeTelemetry(tenant, []byte{'N', 'L', '1', 2, 0x28, 0xb5})
	assertNoFrame(t, client)
}
