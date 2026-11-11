// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package egress implements third-party data forwarding from ADR-0018.
// Rule-engine "forward" nodes publish an envelope onto one bounded JetStream
// stream per forwarder (subject nl.egress.<tenant>.<forwarder>); an in-process
// worker consumes each stream, encodes the envelope and delivers it through a
// pluggable sink.
//
// Isolation design (a faulty receiver must never affect NL):
//   - Producers only publish to JetStream with a short timeout; they never
//     wait on a receiver.
//   - Every forwarder owns an independent, size/time-bounded stream, so one
//     slow receiver only grows its own capped backlog.
//   - Delivery uses a hard per-call deadline, a small bounded worker pool and
//     a circuit breaker; exhausted or terminal messages move to forwarder_dlq
//     instead of being retried forever.
//   - No database connection is held while blocked on external I/O.
package egress

import (
	"errors"
	"time"
)

// Sink type identifiers stored in forwarder.sink_type. Slice 1 ships the
// HTTP sink; further kinds are added in later milestones behind the same
// Sink interface.
const (
	// SinkHTTP delivers to an HTTP/HTTPS webhook.
	SinkHTTP = "http"
	// SinkMQTT publishes to an MQTT broker topic.
	SinkMQTT = "mqtt"
	// SinkNATS publishes to a NATS subject on an external NATS server.
	SinkNATS = "nats"
	// SinkAMQP publishes to a RabbitMQ exchange/routing key.
	SinkAMQP = "amqp"
	// SinkKafka produces to a Kafka topic.
	SinkKafka = "kafka"
	// SinkEmail sends an email through the tenant-configured SMTP server.
	SinkEmail = "email"
	// SinkSMS sends a text through a generic HTTP SMS provider.
	SinkSMS = "sms"
)

// ErrSinkUnknown means no factory is registered for a sink type.
var ErrSinkUnknown = errors.New("unknown forwarder sink")

// Encoder identifiers. Slice 1 supports JSON only.
const (
	// EncoderJSON encodes the envelope as a JSON object.
	EncoderJSON = "json"
)

// Envelope is the JSON frame carried on the egress stream. It mirrors the
// rule-engine message that produced it. Attempt is not authoritative: the
// worker derives the delivery count from the JetStream message metadata.
type Envelope struct {
	// ID uniquely identifies this forwarding event.
	ID string `json:"id"`
	// TenantID owns the originating device.
	TenantID string `json:"tenant_id"`
	// DeviceID is the originating device.
	DeviceID string `json:"device_id"`
	// Ts is the event timestamp in milliseconds.
	Ts int64 `json:"ts"`
	// TraceID joins traces across the chain and the forward.
	TraceID string `json:"trace_id,omitempty"`
	// Channel is the business envelope metadata. The forwarder still selects the sink.
	Channel string `json:"channel,omitempty"`
	// Data is the message body being forwarded.
	Data map[string]any `json:"data"`
	// Metadata carries read-only transport context.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Forwarder is one tenant-scoped forwarder configuration row.
type Forwarder struct {
	// ID is the forwarder id, also used in its subject/stream name.
	ID string `json:"id"`
	// TenantID owns the forwarder.
	TenantID string `json:"tenant_id"`
	// Name is the unique (within tenant) display name.
	Name string `json:"name"`
	// SinkType selects the delivery sink.
	SinkType string `json:"sink_type"`
	// SinkConfig holds sink-specific parameters.
	SinkConfig map[string]any `json:"sink_config"`
	// Encoder selects the output encoding.
	Encoder string `json:"encoder"`
	// MaxAttempts bounds retries before a message is moved to the DLQ.
	MaxAttempts int `json:"max_attempts"`
	// MessageTTLMS bounds how long an undelivered message is retained
	// before it expires and is dropped.
	MessageTTLMS int64 `json:"message_ttl_ms"`
	// MaxBufferMessages caps the messages held for this forwarder; when
	// full the oldest are discarded.
	MaxBufferMessages int `json:"max_buffer_messages"`
	// Enabled turns consumption/delivery on or off.
	Enabled bool `json:"enabled"`
	// CreatedBy records the principal that created the forwarder.
	CreatedBy string `json:"created_by,omitempty"`
	// CreatedAt is the creation timestamp.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last modification timestamp.
	UpdatedAt time.Time `json:"updated_at"`
}

// Input is the write half accepted on create/update.
type Input struct {
	// Name is the forwarder name.
	Name string
	// SinkType selects the sink.
	SinkType string
	// SinkConfig holds sink parameters.
	SinkConfig map[string]any
	// Encoder selects the encoding; empty defaults to JSON.
	Encoder string
	// MaxAttempts bounds retries; zero keeps the stored/default value.
	MaxAttempts int
	// MessageTTLMS bounds retained message age; zero keeps the default.
	MessageTTLMS int64
	// MaxBufferMessages caps held messages; zero keeps the default.
	MaxBufferMessages int
	// Enabled sets the on/off flag.
	Enabled bool
}

// DeadLetter is one envelope parked after terminal failure or exhausted
// retries, with triage metadata.
type DeadLetter struct {
	// ID is the DLQ row id.
	ID string `json:"id"`
	// TenantID owns the row.
	TenantID string `json:"tenant_id"`
	// ForwarderID names the source forwarder.
	ForwarderID string `json:"forwarder_id"`
	// Envelope is the original frame, used verbatim for a replay.
	Envelope Envelope `json:"payload"`
	// Error is the last failure reason.
	Error string `json:"error"`
	// Attempts is the number of deliveries attempted.
	Attempts int `json:"attempts"`
	// LastTriedAt records the final attempt time.
	LastTriedAt *time.Time `json:"last_tried_at,omitempty"`
	// ReplayedAt records a successful manual replay.
	ReplayedAt *time.Time `json:"replayed_at,omitempty"`
	// CreatedAt is when the row was inserted.
	CreatedAt time.Time `json:"created_at"`
}

// DeliveryResult classifies one sink call so the worker chooses the next
// action without interpreting sink internals.
type DeliveryResult int

const (
	// ResultDelivered means the receiver accepted the message; ack it.
	ResultDelivered DeliveryResult = iota
	// ResultRetryable means a transient failure; redeliver after a backoff.
	ResultRetryable
	// ResultTerminal means a permanent/config failure; move to the DLQ.
	ResultTerminal
)

// String returns the stable label persisted in the delivery log and shown
// over the API, so callers never depend on the numeric iota ordering.
func (r DeliveryResult) String() string {
	switch r {
	case ResultDelivered:
		return StatusDelivered
	case ResultRetryable:
		return StatusRetry
	default:
		return StatusTerminal
	}
}

// Outcome reports one sink.Deliver call. It bundles the classification the
// worker acts on with the receiver return code and detail recorded in the
// delivery log, so the HTTP status is observable even though settlement is
// asynchronous.
type Outcome struct {
	// Result classifies the outcome for the worker.
	Result DeliveryResult
	// Code is the transport return code (e.g. HTTP status); 0 when the
	// failure happened before any receiver response.
	Code int
	// Reason is the human-readable detail for the log and DLQ.
	Reason string
}

// Delivery log status labels stored in forwarder_delivery.status.
const (
	// StatusDelivered means the receiver accepted the message.
	StatusDelivered = "delivered"
	// StatusRetry means a transient failure; the message will be retried.
	StatusRetry = "retry"
	// StatusTerminal means a permanent failure; the message moved to DLQ.
	StatusTerminal = "terminal"
)

// Delivery is one bounded row of the per-forwarder delivery log. It records
// the receiver return code for every real delivery attempt. The full payload
// is intentionally not stored here; the envelope id joins to the DLQ row and
// to the live trace, keeping the log small.
type Delivery struct {
	// ID is the delivery log row id.
	ID string `json:"id"`
	// TenantID owns the row.
	TenantID string `json:"tenant_id"`
	// ForwarderID names the source forwarder.
	ForwarderID string `json:"forwarder_id"`
	// EnvelopeID joins the logged attempt to its envelope and trace.
	EnvelopeID string `json:"envelope_id"`
	// DeviceID is the originating device, kept for quick triage.
	DeviceID string `json:"device_id"`
	// Status is one of the Status* labels.
	Status string `json:"status"`
	// Code is the receiver return code (e.g. HTTP status); 0 if none.
	Code int `json:"code"`
	// Reason is the failure detail; empty on success.
	Reason string `json:"reason,omitempty"`
	// Attempts is the delivery number for this envelope.
	Attempts int `json:"attempts"`
	// OccurredAt is when the attempt finished.
	OccurredAt time.Time `json:"occurred_at"`
}

// Delivery retention tuning. The log is operational data, so it is bounded
// globally rather than per tenant configuration.
const (
	// deliveryRetention bounds the age of a delivery log row.
	deliveryRetention = 7 * 24 * time.Hour
	// deliveryCapPerForwarder bounds rows kept for one forwarder; when
	// exceeded the oldest are pruned.
	deliveryCapPerForwarder = 1000
	// deliveryPruneInterval bounds how often the worker prunes the log.
	deliveryPruneInterval = 5 * time.Minute
	// deliveryListLimit bounds rows returned by one log list request.
	deliveryListLimit = 200
)
