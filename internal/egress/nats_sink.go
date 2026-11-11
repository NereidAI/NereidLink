// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nats_sink.go is the built-in sink for an external NATS server. It encodes
// the envelope as JSON, publishes it to one subject and flushes within the
// delivery deadline. The connection is opened lazily on the first delivery,
// because the factory also runs for create/update validation where dialing
// would block the reconcile loop and leak a connection per validation. Once
// up, the client reconnects automatically.

package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// natsConnectTimeout bounds the initial server connection.
const natsConnectTimeout = 5 * time.Second

// natsConfig is the NATS sink configuration.
type natsConfig struct {
	// Servers is one or more comma-separated nats:// URLs.
	Servers string `json:"servers"`
	// Subject is the publish destination.
	Subject string `json:"subject"`
	// Username/Password carry optional user/password authentication.
	Username string `json:"username"`
	Password string `json:"password"`
	// Token carries optional token authentication.
	Token string `json:"token"`
}

// natsSink publishes envelopes to one NATS subject.
type natsSink struct {
	cfg natsConfig
	mu  sync.Mutex
	nc  *nats.Conn
}

func init() {
	Register(SinkNATS, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg natsConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if cfg.Servers == "" {
			return nil, fmt.Errorf("nats sink requires servers")
		}
		if cfg.Subject == "" {
			return nil, fmt.Errorf("nats sink requires subject")
		}
		// No connection yet; it is opened on the first delivery.
		return &natsSink{cfg: cfg}, nil
	})
}

// Deliver publishes the JSON-encoded envelope, connecting on the first call.
// Connect/publish/flush failures are retryable.
func (s *natsSink) Deliver(ctx context.Context, env Envelope) Outcome {
	nc, err := s.ensureConnected(ctx)
	if err != nil {
		return Outcome{Result: ResultRetryable, Reason: "nats connect: " + err.Error()}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
	}
	if err := nc.Publish(s.cfg.Subject, payload); err != nil {
		return Outcome{Result: ResultRetryable, Reason: "nats publish: " + err.Error()}
	}
	// Flush within the deadline so a buffered-but-unsent message is treated
	// as a retry rather than a false success.
	if err := nc.FlushWithContext(ctx); err != nil {
		return Outcome{Result: ResultRetryable, Reason: "nats flush: " + err.Error()}
	}
	return Outcome{Result: ResultDelivered}
}

// ensureConnected dials the external server on first use. It is serialized
// across deliveries. The connection is created at most once: with unlimited
// reconnects the nats client re-establishes the link itself, so a later
// delivery while disconnected reuses the same client instead of dialing a
// second connection (which would leak the old one and race its reconnect).
// Only a fully closed client is replaced.
func (s *natsSink) ensureConnected(ctx context.Context) (*nats.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nc != nil {
		if !s.nc.IsClosed() {
			return s.nc, nil
		}
		s.nc.Close()
		s.nc = nil
	}
	opts := []nats.Option{
		nats.Timeout(natsConnectTimeout),
		nats.MaxReconnects(-1),
	}
	if s.cfg.Username != "" {
		opts = append(opts, nats.UserInfo(s.cfg.Username, s.cfg.Password))
	} else if s.cfg.Token != "" {
		opts = append(opts, nats.Token(s.cfg.Token))
	}
	nc, err := nats.Connect(s.cfg.Servers, opts...)
	if err != nil {
		return nil, err
	}
	s.nc = nc
	return nc, nil
}

// Close closes the external connection when one was opened; called when the
// runner stops or rebuilds the sink.
func (s *natsSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nc != nil {
		s.nc.Close()
		s.nc = nil
	}
	return nil
}

// Compile-time guard that the sink satisfies the interface.
var _ Sink = (*natsSink)(nil)
