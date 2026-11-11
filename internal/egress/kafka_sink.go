// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// kafka_sink.go is the built-in Apache Kafka sink. It encodes the envelope as
// JSON and produces one record to the configured topic synchronously so the
// outcome is known before settlement. franz-go opens broker connections and
// refreshes metadata lazily in the background, so constructing the client in
// the factory neither blocks the reconcile loop nor dials eagerly; the
// produce call itself is bounded by the delivery context.

package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// brokerList accepts either a JSON array of hosts or a single
// comma-separated string, because the forwarder form edits brokers as one
// text field ("host1:9092, host2:9092") while direct API callers may send
// an array.
type brokerList []string

// UnmarshalJSON tolerates both a JSON string and a JSON array of strings.
func (b *brokerList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*b = brokerList(list)
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	for _, item := range strings.Split(single, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	*b = brokerList(list)
	return nil
}

// kafkaConfig is the Kafka sink configuration.
type kafkaConfig struct {
	// Brokers lists the seed broker addresses (host:port).
	Brokers brokerList `json:"brokers"`
	// Topic is the produce destination.
	Topic string `json:"topic"`
	// UseSASL explicitly enables SASL/PLAIN authentication. Older configs
	// that only set Username are treated as SASL-enabled for compatibility.
	UseSASL bool `json:"use_sasl"`
	// Username/Password enable SASL/PLAIN authentication when non-empty.
	Username string `json:"username"`
	Password string `json:"password"`
}

// kafkaSink produces envelopes to one Kafka topic.
type kafkaSink struct {
	client *kgo.Client
	topic  string
}

func init() {
	Register(SinkKafka, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg kafkaConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		// Brokers accept either a JSON array or a comma-separated string
		// (see brokerList.UnmarshalJSON); the form sends the latter.
		if len(cfg.Brokers) == 0 {
			return nil, fmt.Errorf("kafka sink requires brokers")
		}
		if cfg.Topic == "" {
			return nil, fmt.Errorf("kafka sink requires topic")
		}
		saslEnabled := cfg.UseSASL || cfg.Username != ""
		if cfg.UseSASL && cfg.Username == "" {
			return nil, fmt.Errorf("kafka sink requires username when SASL authentication is enabled")
		}

		opts := []kgo.Opt{
			kgo.SeedBrokers([]string(cfg.Brokers)...),
		}
		if saslEnabled {
			opts = append(opts, kgo.SASL(plain.Auth{
				User: cfg.Username,
				Pass: cfg.Password,
			}.AsMechanism()))
		}
		client, err := kgo.NewClient(opts...)
		if err != nil {
			return nil, err
		}
		return &kafkaSink{client: client, topic: cfg.Topic}, nil
	})
}

// Deliver produces the JSON-encoded envelope and waits for acknowledgement,
// bounded by ctx. Unreachable brokers or a produce error are retryable.
func (s *kafkaSink) Deliver(ctx context.Context, env Envelope) Outcome {
	payload, err := json.Marshal(env)
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
	}
	record := &kgo.Record{
		Topic: s.topic,
		Value: payload,
	}
	if env.TraceID != "" {
		record.Headers = append(record.Headers, kgo.RecordHeader{
			Key:   "trace_id",
			Value: []byte(env.TraceID),
		})
	}
	results := s.client.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		return Outcome{Result: ResultRetryable, Reason: "kafka produce: " + err.Error()}
	}
	return Outcome{Result: ResultDelivered}
}

// Close shuts the franz client and its background goroutines down; called by
// the runner when the consumer is stopped or rebuilt.
func (s *kafkaSink) Close() error {
	s.client.Close()
	return nil
}

// Compile-time guard.
var _ Sink = (*kafkaSink)(nil)
