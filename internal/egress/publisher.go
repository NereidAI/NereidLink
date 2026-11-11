// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// publisher.go is the producer-side entry used by rule-engine forward nodes.
// Publishing is fire-and-forget with a short timeout so a slow NATS/JetStream
// can never block the rule chain; callers treat a publish failure as a
// dropped (and observable) event, never as backpressure.

package egress

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// DefaultPublishTimeout bounds a producer publish.
const DefaultPublishTimeout = 2 * time.Second

// Publisher publishes envelopes onto a forwarder's stream.
type Publisher struct {
	js      jetstream.JetStream
	timeout time.Duration
}

// NewPublisher constructs an egress publisher. A non-positive timeout uses
// the default.
func NewPublisher(js jetstream.JetStream, timeout time.Duration) *Publisher {
	if timeout <= 0 {
		timeout = DefaultPublishTimeout
	}
	return &Publisher{js: js, timeout: timeout}
}

// Publish enqueues an envelope for one forwarder.
func (p *Publisher) Publish(
	ctx context.Context,
	forwarderID string,
	env Envelope,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = p.js.Publish(ctx, Subject(env.TenantID, forwarderID), raw)
	return err
}
