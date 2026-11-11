// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// amqp_kafka_sink_test.go verifies RabbitMQ/Kafka sink config validation and
// the unreachable-broker failure path without standing up real brokers.

package egress

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestAMQPSinkValidation checks the factory rejects incomplete config.
func TestAMQPSinkValidation(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType:   SinkAMQP,
		SinkConfig: map[string]any{"routing_key": "q"},
	}); err == nil {
		t.Fatal("expected error for missing url")
	}
	if _, err := buildSink(Forwarder{
		SinkType: SinkAMQP,
		SinkConfig: map[string]any{
			"url": "amqp://127.0.0.1:5672/",
		},
	}); err == nil {
		t.Fatal("expected error for missing routing_key")
	}
}

// TestAMQPSinkDeliverUnreachable verifies dial happens on Deliver and a dead
// broker is classified retryable.
func TestAMQPSinkDeliverUnreachable(t *testing.T) {
	sink, err := buildSink(Forwarder{
		SinkType: SinkAMQP,
		SinkConfig: map[string]any{
			"url":         "amqp://guest:guest@127.0.0.1:1/",
			"routing_key": "nl",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := sink.Deliver(ctx, Envelope{ID: "1"})
	if out.Result != ResultRetryable {
		t.Fatalf("expected retryable, got %d (%s)", out.Result, out.Reason)
	}
}

// TestKafkaSinkValidation checks the factory rejects incomplete config.
func TestKafkaSinkValidation(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType:   SinkKafka,
		SinkConfig: map[string]any{"topic": "t"},
	}); err == nil {
		t.Fatal("expected error for missing brokers")
	}
	if _, err := buildSink(Forwarder{
		SinkType: SinkKafka,
		SinkConfig: map[string]any{
			"brokers": []string{"127.0.0.1:9092"},
		},
	}); err == nil {
		t.Fatal("expected error for missing topic")
	}
}

// TestBrokerListUnmarshal verifies both accepted JSON shapes.
func TestBrokerListUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"string", `"127.0.0.1:9092, 127.0.0.2:9092 ,"`,
			[]string{"127.0.0.1:9092", "127.0.0.2:9092"}},
		{"array", `["127.0.0.1:9092"]`, []string{"127.0.0.1:9092"}},
		{"empty string", `""`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got brokerList
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", []string(got), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", []string(got), tc.want)
				}
			}
		})
	}
}

// TestKafkaSinkBrokersAcceptsString verifies the comma-separated string the
// forwarder form submits builds a sink (regression: unmarshalling a string
// into []string failed before brokerList existed).
func TestKafkaSinkBrokersAcceptsString(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType: SinkKafka,
		SinkConfig: map[string]any{
			"brokers": "127.0.0.1:9092, 127.0.0.2:9092",
			"topic":   "nl",
		},
	}); err != nil {
		t.Fatalf("build with string brokers: %v", err)
	}
}

// TestKafkaSinkBrokersAcceptsArray verifies a JSON array still builds.
func TestKafkaSinkBrokersAcceptsArray(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType: SinkKafka,
		SinkConfig: map[string]any{
			"brokers": []string{"127.0.0.1:9092"},
			"topic":   "nl",
		},
	}); err != nil {
		t.Fatalf("build with array brokers: %v", err)
	}
}

// TestKafkaSinkSASLRequiresUsername checks the explicit SASL switch gates
// the username field.
func TestKafkaSinkSASLRequiresUsername(t *testing.T) {
	if _, err := buildSink(Forwarder{
		SinkType: SinkKafka,
		SinkConfig: map[string]any{
			"brokers":  "127.0.0.1:9092",
			"topic":    "nl",
			"use_sasl": true,
		},
	}); err == nil {
		t.Fatal("expected error when SASL is enabled without username")
	}
}

// TestKafkaSinkSASLConfigAccepted checks both the explicit switch form and
// legacy username-only configuration still build.
func TestKafkaSinkSASLConfigAccepted(t *testing.T) {
	configs := []map[string]any{
		{
			"brokers":  "127.0.0.1:9092",
			"topic":    "nl",
			"use_sasl": true,
			"username": "nluser",
			"password": "secret",
		},
		{
			"brokers":  "127.0.0.1:9092",
			"topic":    "nl",
			"username": "legacy",
		},
	}
	for i, raw := range configs {
		if _, err := buildSink(Forwarder{SinkType: SinkKafka, SinkConfig: raw}); err != nil {
			t.Fatalf("config %d: %v", i, err)
		}
	}
}

// TestKafkaSinkDeliverUnreachable verifies a dead seed broker makes the
// synchronous produce fail and be classified retryable.
func TestKafkaSinkDeliverUnreachable(t *testing.T) {
	sink, err := buildSink(Forwarder{
		SinkType: SinkKafka,
		SinkConfig: map[string]any{
			"brokers": []string{"127.0.0.1:1"},
			"topic":   "nl",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() {
		if closer, ok := sink.(sinkCloser); ok {
			_ = closer.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := sink.Deliver(ctx, Envelope{ID: "1"})
	if out.Result != ResultRetryable {
		t.Fatalf("expected retryable, got %d (%s)", out.Result, out.Reason)
	}
}
