// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package telemetry provides the public, transport-independent uplink
// for southbound adapters, including adapters developed in separate
// repositories.
//
// A JSON telemetry frame is normalized against the device profile
// descriptors, wrapped in one Envelope per measurement point and
// published to the device JetStream partition. Frame parsing, value
// classification (number, boolean, string), descriptor application and
// subject topology therefore have one authoritative implementation,
// shared by embedded ingresses and external adapters.
//
// # Frame value contract
//
// Each field value MUST be a JSON number, boolean or string. Null,
// arrays and arbitrary nested objects are rejected and skipped. A nested
// object is accepted in exactly one shape — the binary media marker:
//
//	{
//	  "photo": {
//	    "blob": true,
//	    "content_type": "image/jpeg",
//	    "filename": "camera-01.jpg",
//	    "data": "/9j/4AAQ..."
//	  }
//	}
//
// "data" is the file payload encoded as base64 (standard or URL-safe
// alphabet, with or without padding); raw plain text is not accepted.
// With a MediaStore configured via WithMediaStore the bytes are
// offloaded into blob storage and the published point carries the string
// reference BlobURIScheme + object key ("blob://<key>"). Without a
// MediaStore the field is rejected; bytes are never inlined into the
// Envelope or the time-series row. Size and content-type limits are the
// MediaStore implementation's responsibility.
package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/partition"
	"github.com/nereidai/nereidlink/sdk/profile"
	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// SubjectPrefix is the subject root for raw device telemetry.
const SubjectPrefix = "nl.telemetry"

// BlobURIScheme prefixes a telemetry string value that references an
// object in blob storage rather than carrying literal text. Read-side
// consumers detect this scheme and resolve the object via a short-lived
// signed URL. Keeping the scheme here guarantees frame producers and
// consumers agree on the literal.
const BlobURIScheme = "blob://"

// MediaStore offloads binary frame values into blob storage and returns
// the object key (without BlobURIScheme). The platform wires the active
// blob backend; a nil media store makes marked fields fail with
// "blob storage not configured". Implementations enforce their own size
// cap and content-type allowlist and must return an error the caller can
// surface (internal/blob.MediaStore is the reference policy).
type MediaStore interface {
	// PutMedia stores data for the device identified by id and returns
	// the blob object key. contentType is the marker's declared type and
	// may be empty; filename is the marker's display-only name. The
	// returned key must be usable unchanged after BlobURIScheme.
	PutMedia(ctx context.Context, id Identity, contentType, filename string, data []byte) (string, error)
}

// EnvelopeSchemaVersion is the current Envelope schema version.
const EnvelopeSchemaVersion uint32 = 1

// Identity identifies the originating device of a frame.
type Identity struct {
	// TenantID owns the device.
	TenantID string

	// DeviceID is the authenticated device identifier.
	DeviceID string
}

// DeviceIdentity is the transport-neutral projection of a successful
// registry authentication.
type DeviceIdentity struct {
	// TenantID owns the authenticated device.
	TenantID string

	// DeviceID is the authenticated device identifier.
	DeviceID string

	// Capabilities lists granted capabilities such as "gateway".
	Capabilities []string
}

// DescriptorResolver resolves the active profile descriptors of a
// device. profile.Resolver satisfies it; adapters may pass nil to carry
// values verbatim.
type DescriptorResolver interface {
	// Resolve returns the profile and its descriptors.
	Resolve(ctx context.Context, tenantID, deviceID string) (profile.Profile, map[string]profile.Descriptor, error)

	// ExpressionRuntime returns the runtime for expression descriptors.
	ExpressionRuntime() *scriptruntime.Runtime
}

// FrameEncoder optionally compresses marshaled Envelope bytes before
// they cross the bus. internal/buscodec.Codec satisfies it. A nil
// encoder means frames are written uncompressed.
type FrameEncoder interface {
	// Encode returns the bus-ready form of raw.
	Encode(raw []byte, contentType string) []byte
}

// publishConfig carries per-Publish options.
type publishConfig struct {
	// encoder compresses each envelope when set.
	encoder FrameEncoder
	// mediaStore offloads marked binary frame fields; nil rejects them.
	mediaStore MediaStore
}

// PublishOption customizes one Publish call.
type PublishOption func(*publishConfig)

// WithEncoder enables envelope compression through encoder.
func WithEncoder(encoder FrameEncoder) PublishOption {
	return func(c *publishConfig) { c.encoder = encoder }
}

// WithMediaStore enables offload of frame fields marked as binary into
// the blob backend.
func WithMediaStore(store MediaStore) PublishOption {
	return func(c *publishConfig) { c.mediaStore = store }
}

// Publish converts a JSON frame into Envelopes and publishes one per
// measurement key. protocol marks the originating transport (MQTT,
// HTTP, COAP or a custom adapter type). Frame fields accept numbers,
// booleans and strings; only numbers run through descriptor transforms.
// It returns the number of points published.
func Publish(ctx context.Context, js jetstream.JetStream,
	resolver DescriptorResolver, id Identity, protocol string,
	frame []byte, now time.Time, opts ...PublishOption) (int, error) {
	cfg := publishConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	// Keep raw values so each field can be classified as number, boolean
	// or string instead of coercing everything onto float64.
	var values map[string]json.RawMessage
	if err := json.Unmarshal(frame, &values); err != nil {
		return 0, fmt.Errorf("decode telemetry frame: %w", err)
	}

	var descriptors map[string]profile.Descriptor
	if resolver != nil {
		_, resolved, err := resolver.Resolve(ctx, id.TenantID, id.DeviceID)
		if err != nil {
			return 0, fmt.Errorf("resolve descriptors: %w", err)
		}
		descriptors = resolved
	}

	part := partition.Of(id.DeviceID)
	subject := subject(partition.Shard(part), id.TenantID, part)
	eventTime := now.UnixMilli()
	published := 0

	for key, raw := range values {
		point, err := pointForRaw(ctx, key, raw, id, cfg.mediaStore, descriptors, resolver)
		if err != nil {
			slog.Warn("skip telemetry field with unsupported value",
				"device", id.DeviceID, "key", key, "error", err)
			continue
		}
		payload, err := proto.Marshal(point)
		if err != nil {
			return published, fmt.Errorf("marshal telemetry point: %w", err)
		}

		env := &v1.Envelope{
			Id:            uuid.NewString(),
			SchemaVersion: EnvelopeSchemaVersion,
			TenantId:      id.TenantID,
			DeviceId:      id.DeviceID,
			Type:          v1.MsgType_MSG_TYPE_TELEMETRY,
			ContentType:   "application/protobuf",
			Payload:       payload,
			EventTime:     eventTime,
			IngressTime:   eventTime,
			Protocol:      protocol,
			PartitionKey:  id.DeviceID,
		}

		rawEnvelope, err := proto.Marshal(env)
		if err != nil {
			return published, fmt.Errorf("marshal envelope: %w", err)
		}
		if cfg.encoder != nil {
			rawEnvelope = cfg.encoder.Encode(rawEnvelope, env.ContentType)
		}
		if _, err := js.Publish(ctx, subject, rawEnvelope); err != nil {
			return published, fmt.Errorf("publish jetstream: %w", err)
		}
		published++
	}
	return published, nil
}

// subject builds the concrete telemetry subject for one partition.
// Layout: nl.telemetry.s<shard>.<tenant>.<partition>. The shard segment
// pins the message to the correct physical stream; the tenant segment
// allows subject-filter subscriptions.
func subject(shard uint32, tenantID string, part uint32) string {
	return fmt.Sprintf("%s.s%d.%s.%d", SubjectPrefix, shard, tenantID, part)
}

// blobMarker is the one structured frame field accepted by the parser.
// Its JSON wire shape is the device-facing contract and MUST stay
// stable:
//
//	{"blob":true,"content_type":"image/jpeg",
//	 "filename":"x.jpg","data":"<base64>"}
type blobMarker struct {
	// Blob must be the boolean true; false rejects the field so an
	// unrelated object is not mistaken for media.
	Blob bool `json:"blob"`
	// ContentType is the declared media type, empty meaning "detect it".
	ContentType string `json:"content_type"`
	// Filename is the optional original name, display only.
	Filename string `json:"filename"`
	// Data is the base64 payload; never literal plain text.
	Data string `json:"data"`
}

// pointForRaw converts one raw frame field into a normalized telemetry
// point. Numbers pass through descriptor conversion; booleans and
// strings are carried verbatim. Null and unrecognized structured values
// are rejected. A {blob:true,...} field is decoded and offloaded through
// mediaStore, producing a string point that references the blob.
func pointForRaw(ctx context.Context, key string, raw json.RawMessage,
	id Identity, mediaStore MediaStore,
	descriptors map[string]profile.Descriptor,
	resolver DescriptorResolver) (*v1.TelemetryPoint, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty value")
	}
	switch trimmed[0] {
	case '[':
		return nil, fmt.Errorf("unsupported structured value")
	case '{':
		return pointForBlobMarker(ctx, key, trimmed, id, mediaStore)
	case 't', 'f':
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("decode boolean: %w", err)
		}
		return &v1.TelemetryPoint{
			MeasurementKey: key,
			VBool:          value,
			Quality:        "GOOD",
			ValueType:      v1.ValueType_VALUE_TYPE_BOOLEAN,
		}, nil
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("decode string: %w", err)
		}
		return &v1.TelemetryPoint{
			MeasurementKey: key,
			VString:        value,
			Quality:        "GOOD",
			ValueType:      v1.ValueType_VALUE_TYPE_STRING,
		}, nil
	case 'n':
		return nil, fmt.Errorf("null value")
	default:
		var value float64
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("decode number: %w", err)
		}
		return pointFor(key, value, descriptors, resolver), nil
	}
}

// pointForBlobMarker validates one {blob:true,...} field, decodes its
// payload and offloads it; the resulting point carries the blob URI in
// v_string.
func pointForBlobMarker(ctx context.Context, key string, raw json.RawMessage,
	id Identity, mediaStore MediaStore) (*v1.TelemetryPoint, error) {
	var marker blobMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil, fmt.Errorf("decode blob marker: %w", err)
	}
	if !marker.Blob {
		return nil, fmt.Errorf("unsupported structured value")
	}
	if marker.Data == "" {
		return nil, fmt.Errorf("blob marker missing data")
	}
	if mediaStore == nil {
		return nil, fmt.Errorf("blob storage not configured")
	}
	data, err := decodeBase64(marker.Data)
	if err != nil {
		return nil, fmt.Errorf("decode blob data: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("blob data is empty")
	}
	objectKey, err := mediaStore.PutMedia(ctx, id, marker.ContentType, marker.Filename, data)
	if err != nil {
		return nil, fmt.Errorf("offload blob: %w", err)
	}
	return &v1.TelemetryPoint{
		MeasurementKey: key,
		VString:        BlobURIScheme + objectKey,
		Quality:        "GOOD",
		ValueType:      v1.ValueType_VALUE_TYPE_STRING,
	}, nil
}

// decodeBase64 decodes standard base64 and falls back to the raw and
// URL-safe alphabets, accepting the padding/charset variants device
// clients commonly emit.
func decodeBase64(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if data, err := enc.DecodeString(value); err == nil {
			return data, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// pointFor converts a raw numeric field into a normalized telemetry
// point, applying the descriptor transform when one exists.
func pointFor(key string, raw float64,
	descriptors map[string]profile.Descriptor,
	resolver DescriptorResolver) *v1.TelemetryPoint {
	point := &v1.TelemetryPoint{
		MeasurementKey: key,
		RawDouble:      raw,
		VDouble:        raw,
		Quality:        "GOOD",
		ValueType:      v1.ValueType_VALUE_TYPE_NUMBER,
	}
	descriptor, ok := descriptors[key]
	if !ok || resolver == nil {
		return point
	}

	point.Unit = descriptor.Unit
	//nolint:gosec // descriptor versions are positive integers
	point.DescriptorVersion = uint32(descriptor.Version)
	result, err := profile.Apply(descriptor, raw, resolver.ExpressionRuntime())
	if err != nil {
		point.Quality = "BAD"
		return point
	}
	if result.IsString {
		point.VString = result.String
		point.ValueType = v1.ValueType_VALUE_TYPE_STRING
	} else {
		point.VDouble = result.Double
	}
	if !result.InRange {
		point.Quality = "BAD"
	}
	return point
}
