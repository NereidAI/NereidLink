// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package contract defines cross-cutting wire identifiers shared by
// multiple services: envelope versions, content types, protocol names and
// data quality markers.
//
// Values here are part of internal message contracts and MUST be defined
// exactly once. Operational tunables belong in configuration instead.
package contract

const (
	// EnvelopeSchemaVersion is the current Envelope schema version.
	EnvelopeSchemaVersion uint32 = 1

	// ContentTypeProtobuf marks a protobuf-encoded payload.
	ContentTypeProtobuf = "application/protobuf"

	// ContentTypeJSON marks a JSON-encoded payload.
	ContentTypeJSON = "application/json"

	// ProtocolMQTT identifies the MQTT transport on an Envelope.
	ProtocolMQTT = "MQTT"

	// ProtocolHTTP identifies the HTTP transport on an Envelope.
	ProtocolHTTP = "HTTP"

	// ProtocolCoAP identifies the CoAP transport on an Envelope.
	ProtocolCoAP = "COAP"

	// ListenerTCPID is the stable identifier of the MQTT TCP listener.
	ListenerTCPID = "nl-tcp"

	// ListenerTLSID is the stable identifier of the MQTT mTLS listener.
	ListenerTLSID = "nl-tls"
)

// Data quality markers written with normalized telemetry.
const (
	// QualityGood marks a valid, in-range measurement.
	QualityGood = "GOOD"

	// QualityUncertain marks a questionable but usable measurement.
	QualityUncertain = "UNCERTAIN"

	// QualityBad marks an invalid or unusable measurement.
	QualityBad = "BAD"
)
