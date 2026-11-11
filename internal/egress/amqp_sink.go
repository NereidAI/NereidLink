// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// amqp_sink.go is the built-in RabbitMQ sink (AMQP 0-9-1). It encodes the
// envelope as JSON and publishes it to one exchange/routing key. The broker
// connection and channel are opened lazily on the first delivery, because the
// factory also runs for create/update validation where dialing would block
// reconcile and leak a connection per validation. After a publish error the
// link is dropped so the next delivery reconnects.

package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// amqpDialTimeout bounds the initial broker TCP connection.
const amqpDialTimeout = 5 * time.Second

// amqpConfig is the RabbitMQ sink configuration.
type amqpConfig struct {
	// URL is the AMQP URL, e.g. amqp://user:pass@host:5672/vhost.
	URL string `json:"url"`
	// Exchange is the target exchange; "" means the default exchange.
	Exchange string `json:"exchange"`
	// RoutingKey selects the queue(s).
	RoutingKey string `json:"routing_key"`
}

// amqpSink publishes envelopes to one RabbitMQ exchange.
type amqpSink struct {
	cfg  amqpConfig
	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

func init() {
	Register(SinkAMQP, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg amqpConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if cfg.URL == "" {
			return nil, fmt.Errorf("amqp sink requires url")
		}
		if cfg.RoutingKey == "" {
			return nil, fmt.Errorf("amqp sink requires routing_key")
		}
		// Connection opened lazily.
		return &amqpSink{cfg: cfg}, nil
	})
}

// Deliver publishes the JSON-encoded envelope, connecting on the first call.
// Connection/publish failures are retryable.
func (s *amqpSink) Deliver(ctx context.Context, env Envelope) Outcome {
	ch, err := s.channel(ctx)
	if err != nil {
		return Outcome{Result: ResultRetryable, Reason: "amqp connect: " + err.Error()}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
	}
	err = ch.PublishWithContext(ctx,
		s.cfg.Exchange, s.cfg.RoutingKey,
		false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         payload,
		})
	if err != nil {
		// Drop the dead link so the next delivery reconnects instead of
		// reusing a closed channel.
		s.reset()
		return Outcome{Result: ResultRetryable, Reason: "amqp publish: " + err.Error()}
	}
	return Outcome{Result: ResultDelivered}
}

// channel returns a usable publish channel, dialing and opening the channel
// on first use.
func (s *amqpSink) channel(_ context.Context) (*amqp.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		return s.ch, nil
	}
	conn, err := amqp.DialConfig(s.cfg.URL, amqp.Config{
		// Bound the dial so an unreachable broker cannot hold the worker.
		Dial: func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, amqpDialTimeout)
		},
	})
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	s.conn, s.ch = conn, ch
	return ch, nil
}

// reset closes and clears the link after a publish failure.
func (s *amqpSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn, s.ch = nil, nil
}

// Close shuts the broker connection when one was opened; called when the
// runner stops or rebuilds the sink.
func (s *amqpSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		err := s.conn.Close()
		s.conn, s.ch = nil, nil
		return err
	}
	return nil
}

// Compile-time guard.
var _ Sink = (*amqpSink)(nil)
