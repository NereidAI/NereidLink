// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// mqtt_nats_sink_test.go verifies MQTT/NATS sink configuration validation and
// the lazy-connect failure path without standing up external brokers.

package egress

import (
	"context"
	"testing"
	"time"
)

// TestMQTTSinkValidation checks the factory rejects incomplete config.
func TestMQTTSinkValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"missing broker", map[string]any{"topic": "t"}},
		{"missing topic", map[string]any{"broker": "tcp://127.0.0.1:1883"}},
		{"bad qos", map[string]any{
			"broker": "tcp://127.0.0.1:1883", "topic": "t", "qos": 3,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildSink(Forwarder{
				SinkType: SinkMQTT, SinkConfig: tc.cfg,
			}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// TestMQTTSinkDeliverUnreachable verifies the connection is attempted only on
// Deliver and an unreachable broker is classified retryable, not terminal.
func TestMQTTSinkDeliverUnreachable(t *testing.T) {
	sink, err := buildSink(Forwarder{
		SinkType: SinkMQTT,
		SinkConfig: map[string]any{
			"broker": "tcp://127.0.0.1:1", "topic": "nl/test", "qos": 0,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := sink.Deliver(ctx, Envelope{ID: "1"})
	if out.Result != ResultRetryable {
		t.Fatalf("expected retryable, got %d (%s)", out.Result, out.Reason)
	}
}

// TestNATSSinkValidation checks the factory rejects incomplete config.
func TestNATSSinkValidation(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType:   SinkNATS,
		SinkConfig: map[string]any{"subject": "t"},
	}); err == nil {
		t.Fatal("expected error for missing servers")
	}
	if _, err := buildSink(Forwarder{
		SinkType:   SinkNATS,
		SinkConfig: map[string]any{"servers": "nats://127.0.0.1:4222"},
	}); err == nil {
		t.Fatal("expected error for missing subject")
	}
}

// TestNATSSinkDeliverUnreachable verifies dial happens on Deliver and a dead
// server is classified retryable.
func TestNATSSinkDeliverUnreachable(t *testing.T) {
	sink, err := buildSink(Forwarder{
		SinkType: SinkNATS,
		SinkConfig: map[string]any{
			"servers": "nats://127.0.0.1:1", "subject": "nl.test",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := sink.Deliver(ctx, Envelope{ID: "1"})
	if out.Result != ResultRetryable {
		t.Fatalf("expected retryable, got %d (%s)", out.Result, out.Reason)
	}
}
