// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package coap implements the CoAP southbound module. Devices publish
// telemetry to /telemetry, fetch commands from /commands (long-lived GET
// drained by repeated requests) and acknowledge request-response commands
// to /cmdres/<id>. Authentication maps the device token, carried as a URI
// query or Proxy-Authorization option depending on the client, onto the
// registry; like the HTTP ingress every request refreshes a presence
// session and idle sessions expire through the shared session sweeper.
package coap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/adapter"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// Authenticator resolves a device token to its identity. It is satisfied
// by the registry gRPC client.
type Authenticator interface {
	// Authenticate verifies the token (username) and optional secret.
	Authenticate(ctx context.Context, username, password string) (sdktelemetry.DeviceIdentity, bool, error)
}

// Server owns the CoAP listener and shared module session state.
type Server struct {
	auth     Authenticator
	presence PresenceManager
	sessions *adapter.Sessions
	relay    *adapter.MailboxRelay
	publish  TelemetryPublisher

	listen   string
	longPoll time.Duration
	maxBytes int64
}

// TelemetryPublisher publishes a frame through the shared uplink path and
// returns the number of emitted points.
type TelemetryPublisher func(ctx context.Context, identity adapter.SessionIdentity,
	frame []byte, now time.Time) (int, error)

// Config tunes a CoAP server.
type Config struct {
	// Listen is the plain UDP bind address.
	Listen string

	// LongPoll bounds one GET commands request.
	LongPoll time.Duration

	// SessionTTL bounds idle session lifetime.
	SessionTTL time.Duration

	// SweepInterval paces the idle session sweeper.
	SweepInterval time.Duration

	// MailboxSize bounds buffered commands per device.
	MailboxSize int

	// MaxBytes bounds an accepted request body.
	MaxBytes int64
}

// PresenceManager connects and disconnects presence sessions. It is
// satisfied by the presence service over NATS.
type PresenceManager interface {
	Connect(ctx context.Context, id adapter.SessionIdentity) (string, error)
	Disconnect(ctx context.Context, id adapter.SessionIdentity, sessionID string) error
}

// NewServer constructs the CoAP module server. presence is wired by the
// command entry point after construction alongside the relay.
func NewServer(cfg Config, auth Authenticator, publisher TelemetryPublisher) *Server {
	return &Server{
		auth:     auth,
		sessions: adapter.NewSessions(cfg.MailboxSize),
		publish:  publisher,
		listen:   cfg.Listen,
		longPoll: cfg.LongPoll,
		maxBytes: cfg.MaxBytes,
	}
}

// Sessions exposes the shared session registry to the relay and sweeper.
func (s *Server) Sessions() *adapter.Sessions {
	return s.sessions
}

// SetRelay wires the command relay.
func (s *Server) SetRelay(relay *adapter.MailboxRelay) {
	s.relay = relay
}

// SetPresence wires the presence manager, used to connect and disconnect
// device sessions.
func (s *Server) SetPresence(presence PresenceManager) {
	s.presence = presence
}

// Router builds the CoAP request router.
func (s *Server) Router() *mux.Router {
	router := mux.NewRouter()
	// Static routes never collide, but mux.Handle reports duplicate paths;
	// fail construction loudly instead of dropping the error.
	mustHandle := func(path string, handler mux.Handler) {
		if err := router.Handle(path, handler); err != nil {
			panic(fmt.Errorf("register coap route %s: %w", path, err))
		}
	}
	mustHandle("/telemetry", mux.HandlerFunc(s.handleTelemetry))
	mustHandle("/commands", mux.HandlerFunc(s.handleCommands))
	mustHandle("/cmdres/{id}", mux.HandlerFunc(s.handleCommandResponse))
	return router
}

// Run serves UDP requests until ctx is canceled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	l, err := coapnet.NewListenUDP("udp", s.listen)
	if err != nil {
		return fmt.Errorf("listen coap: %w", err)
	}
	defer func() { _ = l.Close() }()

	server := udp.NewServer(options.WithMux(s.Router()))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(l) }()

	select {
	case <-ctx.Done():
		server.Stop()
		// Serve returns once Stop closes the listener; its shutdown error
		// is expected and the context error is the one to report.
		if serveShutdownErr := <-serveErr; serveShutdownErr != nil {
			slog.Debug("coap server stopped", "error", serveShutdownErr)
		}
		return ctx.Err()
	case err := <-serveErr:
		server.Stop()
		return err
	}
}

// writeError responds with a CoAP status and a JSON error document.
func writeError(w mux.ResponseWriter, code codes.Code, detail string) {
	body := fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code.String(), detail)
	if err := w.SetResponse(code, message.TextPlain, strReader(body)); err != nil {
		_ = err
	}
}

// replyJSON responds with the given status and a JSON body.
func replyJSON(w mux.ResponseWriter, code codes.Code, body string) {
	if err := w.SetResponse(code, message.AppJSON, strReader(body)); err != nil {
		_ = err
	}
}

// sessionFor authenticates the request and ensures a live session. It
// writes an error response and returns false on any failure.
func (s *Server) sessionFor(w mux.ResponseWriter, r *mux.Message) (*adapter.Session, adapter.SessionIdentity, bool) {
	token := tokenFromRequest(r)
	if token == "" {
		writeError(w, codes.Unauthorized, "missing access token")
		return nil, adapter.SessionIdentity{}, false
	}
	identity, ok, err := s.auth.Authenticate(r.Context(), token, "")
	if err != nil {
		writeError(w, codes.InternalServerError, "authentication failed")
		return nil, adapter.SessionIdentity{}, false
	}
	if !ok {
		writeError(w, codes.Unauthorized, "invalid access token")
		return nil, adapter.SessionIdentity{}, false
	}
	id := adapter.SessionIdentity{TenantID: identity.TenantID, DeviceID: identity.DeviceID}

	session, created := s.sessions.Ensure(id, time.Now().UTC())
	if created {
		sessionID, err := s.presence.Connect(r.Context(), id)
		if err != nil {
			s.sessions.Remove(id)
			writeError(w, codes.InternalServerError, "failed to open session")
			return nil, adapter.SessionIdentity{}, false
		}
		s.sessions.SetSessionID(id, sessionID)
	}
	return session, id, true
}

// commandFrame is the JSON encoding of one dispatched command.
type commandFrame struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

// commandsResponse is the command envelope. Command is nil on an idle poll.
type commandsResponse struct {
	Command *commandFrame `json:"command"`
}

var _ = v1.Command{}
