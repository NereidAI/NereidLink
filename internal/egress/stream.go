// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stream.go manages one bounded JetStream stream per forwarder. Keeping the
// stream independent isolates each receiver: a slow sink only fills its own
// capped stream and can never affect other forwarders or the core bus.

package egress

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// SubjectPrefix is the root of egress subjects.
const SubjectPrefix = "nl.egress"

// maxBufferBytes is an ultimate per-stream byte safety. It is generous
// enough not to bind before max_buffer_messages for normal envelopes.
const maxBufferBytes int64 = 256 << 20

// StreamLimits bounds the stored backlog of one forwarder.
type StreamLimits struct {
	// MaxMsgs caps the message count.
	MaxMsgs int64
	// MaxBytes caps the total bytes.
	MaxBytes int64
	// MaxAge caps how long a message is retained.
	MaxAge time.Duration
}

// streamLimits builds the JetStream caps from the forwarder policy.
func (f Forwarder) streamLimits() StreamLimits {
	return StreamLimits{
		MaxMsgs:  int64(f.MaxBufferMessages),
		MaxBytes: maxBufferBytes,
		MaxAge:   time.Duration(f.MessageTTLMS) * time.Millisecond,
	}
}

// Subject returns the egress subject for one forwarder.
// Layout: nl.egress.<tenant>.<forwarder>.
func Subject(tenantID, forwarderID string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectPrefix, tenantID, forwarderID)
}

// StreamName returns the physical JetStream stream name for one forwarder.
func StreamName(tenantID, forwarderID string) string {
	return fmt.Sprintf("EGRESS_%s_%s", tenantID, forwarderID)
}

// EnsureStream creates or updates the bounded stream for a forwarder using
// its configured TTL and buffer policy.
func EnsureStream(
	ctx context.Context,
	js jetstream.JetStream,
	f Forwarder,
	replicas int,
) error {
	if replicas < 1 {
		replicas = 1
	}
	limits := f.streamLimits()
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName(f.TenantID, f.ID),
		Subjects:  []string{Subject(f.TenantID, f.ID)},
		Retention: jetstream.LimitsPolicy,
		// Drop the oldest when the cap is reached so a producer is never
		// blocked; the worker breaker moves open messages to the DLQ well
		// before this normally happens.
		Discard:  jetstream.DiscardOld,
		Replicas: replicas,
		MaxMsgs:  limits.MaxMsgs,
		MaxBytes: limits.MaxBytes,
		MaxAge:   limits.MaxAge,
	})
	if err != nil {
		return fmt.Errorf("ensure egress stream: %w", err)
	}
	return nil
}

// DeleteStream removes the forwarder's stream. A missing stream is ignored so
// deletion stays idempotent.
func DeleteStream(
	ctx context.Context,
	js jetstream.JetStream,
	tenantID, forwarderID string,
) error {
	err := js.DeleteStream(ctx, StreamName(tenantID, forwarderID))
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete egress stream: %w", err)
	}
	return nil
}
