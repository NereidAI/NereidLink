// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sink.go defines the pluggable delivery interface and the registry that
// maps a forwarder.sink_type to its factory. New sinks (MQTT, NATS, Kafka,
// RabbitMQ, email, SMS) register here without touching the worker, so the
// timeout, breaker, backoff and DLQ behavior is shared by every sink.

package egress

import (
	"context"
	"fmt"
	"sync"
)

// Sink delivers one encoded envelope to a receiver.
type Sink interface {
	// Deliver sends the envelope and reports an Outcome bundling the
	// classification, receiver return code and detail. Implementations
	// must honor ctx.
	Deliver(ctx context.Context, env Envelope) Outcome
}

// Factory builds a sink from its stored configuration.
type Factory func(config map[string]any) (Sink, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register registers a sink factory under a sink type. It panics on a
// duplicate registration to surface wiring mistakes at startup.
func Register(kind string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[kind]; exists {
		panic("egress: duplicate sink registration: " + kind)
	}
	registry[kind] = factory
}

// buildSink constructs the sink for a forwarder.
func buildSink(f Forwarder) (Sink, error) {
	registryMu.RLock()
	factory, ok := registry[f.SinkType]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSinkUnknown, f.SinkType)
	}
	sink, err := factory(f.SinkConfig)
	if err != nil {
		return nil, fmt.Errorf("build %s sink: %w", f.SinkType, err)
	}
	return sink, nil
}
