// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// mqtt_sink.go is the built-in MQTT broker sink. It encodes the envelope as
// JSON and publishes it to one configured topic with the chosen QoS. The
// broker connection is opened lazily on the first delivery rather than in the
// factory, because the factory also runs for create/update validation: eager
// dialing there would block the worker reconcile loop and leak a client for
// every validation.
//
// Connection lifecycle: several worker goroutines call Deliver concurrently,
// but paho forbids overlapping Connect calls — a second Connect while the
// first attempt is still in flight fails its token with "status can only
// transition to connecting from disconnected" and the client then stays
// unusable. ensureConnected therefore runs at most one Connect attempt at a
// time; concurrent deliveries wait on the attempt instead of dialing again.
// After the first successful connect paho owns reconnection (AutoReconnect),
// so on a later drop we must not call Connect either — waiters block until
// the OnConnect callback reports the link is back.

package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

// mqttConnectTimeout bounds a single broker dial attempt.
const mqttConnectTimeout = 5 * time.Second

// mqttConfig is the MQTT sink configuration.
type mqttConfig struct {
	// Broker is the primary broker URL, e.g. tcp://host:1883.
	Broker string `json:"broker"`
	// Brokers optionally lists additional URLs for failover.
	Brokers []string `json:"brokers"`
	// Topic is the publish destination.
	Topic string `json:"topic"`
	// QoS is the MQTT quality of service, 0..2.
	QoS int `json:"qos"`
	// ClientID identifies the connection; one is generated when empty.
	ClientID string `json:"client_id"`
	// Username/Password carry optional basic authentication.
	Username string `json:"username"`
	Password string `json:"password"`
}

// mqttSink publishes envelopes to one MQTT topic.
type mqttSink struct {
	cfg mqttConfig

	mu sync.Mutex
	// client is the paho client, constructed once in the factory.
	client mqtt.Client
	// connecting means a Connect call is currently in flight.
	connecting bool
	// ready is closed when the in-flight Connect attempt settles.
	ready chan struct{}
	// attemptStarted stamps the current/latest Connect attempt so a failed
	// initial dial is retried on a fixed cadence instead of a busy loop.
	attemptStarted time.Time
	// changed is closed and replaced on every connection-state transition
	// (established or lost), waking every blocked waiter at once.
	changed chan struct{}
	// everUp records at least one successful connect; from then on paho's
	// AutoReconnect owns re-establishment and Connect must not be reused.
	everUp bool
	// closed is set by Close so callbacks stop touching waiters.
	closed bool
	// lastErr is the error of the most recent connect attempt.
	lastErr error
}

func init() {
	Register(SinkMQTT, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg mqttConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if cfg.Broker == "" && len(cfg.Brokers) == 0 {
			return nil, fmt.Errorf("mqtt sink requires broker")
		}
		if cfg.Topic == "" {
			return nil, fmt.Errorf("mqtt sink requires topic")
		}
		if cfg.QoS < 0 || cfg.QoS > 2 {
			return nil, fmt.Errorf("mqtt qos must be 0..2")
		}
		if cfg.ClientID == "" {
			cfg.ClientID = "nl-egress-" + uuid.NewString()
		}
		s := &mqttSink{cfg: cfg, changed: make(chan struct{})}
		// Construct only; no socket is opened until the first Deliver.
		// Callbacks are wired first so the very first connect is tracked.
		s.client = mqtt.NewClient(s.clientOptions())
		return s, nil
	})
}

// clientOptions builds the paho client options from the sink config.
func (s *mqttSink) clientOptions() *mqtt.ClientOptions {
	cfg := s.cfg
	opts := mqtt.NewClientOptions()
	if cfg.Broker != "" {
		opts.AddBroker(cfg.Broker)
	}
	for _, broker := range cfg.Brokers {
		opts.AddBroker(broker)
	}
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	opts.SetConnectTimeout(mqttConnectTimeout)
	// Dial the configured broker directly. paho otherwise routes plain-TCP
	// dials through ALL_PROXY/HTTPS_PROXY from the process environment, which
	// silently black-holes private/operator brokers reachable directly.
	opts.SetCustomOpenConnectionFn(openMQTTConnection)
	// Reconnect automatically after the first successful connect so a
	// transient broker bounce needs no redelivery.
	opts.SetAutoReconnect(true)
	opts.OnConnect = func(_ mqtt.Client) { s.markUp(nil) }
	opts.OnConnectionLost = func(_ mqtt.Client, err error) { s.markDown(err) }
	return opts
}

// openMQTTConnection dials the broker without consulting proxy environment
// variables: a sink broker is an operator-configured direct endpoint, and a
// shell proxy (ALL_PROXY) must not silently capture it.
func openMQTTConnection(uri *url.URL, options mqtt.ClientOptions) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: mqttConnectTimeout}
	switch uri.Scheme {
	case "ssl", "tls", "mqtts", "mqtt+ssl", "tcps":
		return tls.DialWithDialer(dialer, "tcp", uri.Host, options.TLSConfig)
	default:
		return dialer.Dial("tcp", uri.Host)
	}
}

// markUp records a successful connect and wakes every waiter. It is safe to
// call both from the OnConnect callback and from settle (idempotent).
func (s *mqttSink) markUp(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.everUp = true
	s.connecting = false
	s.lastErr = err
	s.finishAttemptLocked()
	s.broadcastLocked()
}

// markDown records that an established link was lost. paho's AutoReconnect is
// already retrying; waiters are woken to observe the state and re-wait.
func (s *mqttSink) markDown(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastErr = err
	s.broadcastLocked()
}

// settle records the outcome of a Connect call we initiated.
func (s *mqttSink) settle(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.connecting = false
	s.lastErr = err
	s.finishAttemptLocked()
	s.broadcastLocked()
}

// finishAttemptLocked closes the in-flight attempt signal exactly once.
func (s *mqttSink) finishAttemptLocked() {
	if s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
}

// broadcastLocked wakes every current waiter by replacing the signal channel.
func (s *mqttSink) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Deliver publishes the JSON-encoded envelope, opening the broker connection
// on the first call. Broker/connect failures are retryable.
func (s *mqttSink) Deliver(ctx context.Context, env Envelope) Outcome {
	if err := s.ensureConnected(ctx); err != nil {
		return Outcome{Result: ResultRetryable, Reason: "mqtt connect: " + err.Error()}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
	}

	// Clamp before the narrowing conversion: paho QoS is a byte and only
	// 0-2 are valid; an out-of-range config must not wrap silently.
	qos := s.cfg.QoS
	if qos < 0 {
		qos = 0
	} else if qos > 2 {
		qos = 2
	}
	token := s.client.Publish(s.cfg.Topic, byte(qos), false, payload)
	select {
	case <-token.Done():
	case <-ctx.Done():
		return Outcome{Result: ResultRetryable, Reason: "mqtt publish: " + ctx.Err().Error()}
	}
	if err := token.Error(); err != nil {
		return Outcome{Result: ResultRetryable, Reason: "mqtt publish: " + err.Error()}
	}
	return Outcome{Result: ResultDelivered}
}

// errSinkClosed is returned to waiters when the sink is stopped while they
// block on a connection.
var errSinkClosed = errors.New("mqtt sink closed")

// ensureConnected returns when the broker link is up, coalescing concurrent
// deliveries onto a single Connect attempt. It never calls Connect while an
// attempt is in flight or while paho's AutoReconnect owns the link.
func (s *mqttSink) ensureConnected(ctx context.Context) error {
	for {
		if s.client.IsConnected() {
			return nil
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errSinkClosed
		}
		changed := s.changed

		// After the first successful connect, reconnection is paho's job:
		// another explicit Connect would be rejected by its state machine.
		if s.everUp {
			waitErr := s.lastErr
			s.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				if waitErr != nil {
					return fmt.Errorf("%v (waiting for auto-reconnect)", waitErr)
				}
				return ctx.Err()
			}
		}

		// An initial connect attempt is already running; wait for it.
		if s.connecting {
			ready := s.ready
			s.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-changed:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Start the one and only in-flight attempt, pacing retries so an
		// unreachable broker fails fast but does not busy-loop.
		if since := time.Since(s.attemptStarted); since < mqttConnectTimeout {
			wait := mqttConnectTimeout - since
			s.mu.Unlock()
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
				continue
			case <-changed:
				timer.Stop()
				continue
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}

		s.connecting = true
		s.attemptStarted = time.Now()
		s.ready = make(chan struct{})
		token := s.client.Connect()
		ready := s.ready
		s.mu.Unlock()

		go func() {
			<-token.Done()
			s.settle(token.Error())
		}()

		select {
		case <-ready:
			// Loop: IsConnected confirms whether the attempt succeeded.
			continue
		case <-ctx.Done():
			// Do NOT reconnect ourselves; the attempt settles on its own
			// and future deliveries coalesce onto it.
			return ctx.Err()
		}
	}
}

// Close disconnects the broker, letting in-flight publishes finish briefly;
// called when the runner stops or rebuilds the sink.
func (s *mqttSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	client := s.client
	s.finishAttemptLocked()
	s.broadcastLocked()
	s.mu.Unlock()

	client.Disconnect(250)
	return nil
}

// Compile-time guard that the sink satisfies the interface.
var _ Sink = (*mqttSink)(nil)
